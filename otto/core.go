package otto

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/hashicorp/otto/app"
	"github.com/hashicorp/otto/appfile"
	"github.com/hashicorp/otto/context"
	"github.com/hashicorp/otto/directory"
	"github.com/hashicorp/otto/infrastructure"
	"github.com/hashicorp/otto/ui"
	"github.com/hashicorp/terraform/dag"
)

// Core is the main struct to use to interact with Otto as a library.
type Core struct {
	appfile         *appfile.File
	appfileCompiled *appfile.Compiled
	apps            map[app.Tuple]app.Factory
	dir             directory.Backend
	infras          map[string]infrastructure.Factory
	dataDir         string
	localDir        string
	compileDir      string
	ui              ui.Ui
}

// CoreConfig is configuration for creating a new core with NewCore.
type CoreConfig struct {
	// DataDir is the directory where local data will be stored that
	// is global to all Otto processes.
	//
	// LocalDir is the directory where data local to this single Appfile
	// will be stored. This isn't necessarilly cleared for compilation.
	//
	// CompiledDir is the directory where compiled data will be written.
	// Each compilation will clear this directory.
	DataDir    string
	LocalDir   string
	CompileDir string

	// Appfile is the appfile that this core will be using for configuration.
	// This must be a compiled Appfile.
	Appfile *appfile.Compiled

	// Directory is the directory where data is stored about this Appfile.
	Directory directory.Backend

	// Apps is the map of available app implementations.
	Apps map[app.Tuple]app.Factory

	// Infrastructures is the map of available infrastructures. The
	// value is a factory that can create the infrastructure impl.
	Infrastructures map[string]infrastructure.Factory

	// Ui is the Ui that will be used to communicate with the user.
	Ui ui.Ui
}

// NewCore creates a new core.
//
// Once this function is called, this CoreConfig should not be used again
// or modified, since the Core may use parts of it without deep copying.
func NewCore(c *CoreConfig) (*Core, error) {
	return &Core{
		appfile:         c.Appfile.File,
		appfileCompiled: c.Appfile,
		apps:            c.Apps,
		dir:             c.Directory,
		infras:          c.Infrastructures,
		dataDir:         c.DataDir,
		localDir:        c.LocalDir,
		compileDir:      c.CompileDir,
		ui:              c.Ui,
	}, nil
}

// Compile takes the Appfile and compiles all the resulting data.
func (c *Core) Compile() error {
	// Get the infra implementation for this
	infra, infraCtx, err := c.infra()
	if err != nil {
		return err
	}

	// Delete the prior output directory
	log.Printf("[INFO] deleting prior compilation contents: %s", c.compileDir)
	if err := os.RemoveAll(c.compileDir); err != nil {
		return err
	}

	// Compile the infrastructure for our application
	log.Printf("[INFO] running infra compile...")
	if _, err := infra.Compile(infraCtx); err != nil {
		return err
	}

	// Walk through the dependencies and compile all of them.
	// We have to compile every dependency for dev building.
	var resultLock sync.Mutex
	results := make([]*app.CompileResult, 0, len(c.appfileCompiled.Graph.Vertices()))
	err = c.walk(func(app app.App, ctx *app.Context, root bool) error {
		if !root {
			c.ui.Message(fmt.Sprintf(
				"Compiling dependency '%s'...",
				ctx.Appfile.Application.Name))
		} else {
			c.ui.Message(fmt.Sprintf(
				"Compiling main application..."))
		}

		// If this is the root, we set the dev dep fragments.
		if root {
			// We grab the lock just in case although if we're the
			// root this should be serialized.
			resultLock.Lock()
			ctx.DevDepFragments = make([]string, 0, len(results))
			for _, result := range results {
				if result.DevDepFragmentPath != "" {
					ctx.DevDepFragments = append(
						ctx.DevDepFragments, result.DevDepFragmentPath)
				}
			}
			resultLock.Unlock()
		}

		// Compile!
		result, err := app.Compile(ctx)
		if err != nil {
			return err
		}

		// Store the compilation result for later
		resultLock.Lock()
		defer resultLock.Unlock()
		results = append(results, result)

		return nil
	})

	return nil
}

func (c *Core) walk(f func(app.App, *app.Context, bool) error) error {
	root, err := c.appfileCompiled.Graph.Root()
	if err != nil {
		return fmt.Errorf(
			"Error loading app: %s", err)
	}

	// Walk the appfile graph.
	var stop int32 = 0
	return c.appfileCompiled.Graph.Walk(func(raw dag.Vertex) (err error) {
		// If we're told to stop (something else had an error), then stop early.
		// Graphs walks by default will complete all disjoint parts of the
		// graph before failing, but Otto doesn't have to do that.
		if atomic.LoadInt32(&stop) != 0 {
			return nil
		}

		// If we exit with an error, then mark the stop atomic
		defer func() {
			if err != nil {
				atomic.StoreInt32(&stop, 1)
			}
		}()

		// Convert to the rich vertex type so that we can access data
		v := raw.(*appfile.CompiledGraphVertex)

		// Get the context and app for this appfile
		appCtx, err := c.appContext(v.File)
		if err != nil {
			return fmt.Errorf(
				"Error loading Appfile for '%s': %s",
				dag.VertexName(raw), err)
		}
		app, err := c.app(appCtx)
		if err != nil {
			return fmt.Errorf(
				"Error loading App implementation for '%s': %s",
				dag.VertexName(raw), err)
		}

		// Call our callback
		return f(app, appCtx, raw == root)
	})
}

// creds reads the credentials if we have them, or queries the user
// for infrastructure credentials using the infrastructure if we
// don't have them.
func (c *Core) creds(
	infra infrastructure.Infrastructure,
	infraCtx *infrastructure.Context) error {
	// Output to the user some information about what is about to
	// happen here...
	infraCtx.Ui.Header("Detecting infrastructure credentials...")

	// The path to where we put the encrypted creds
	path := filepath.Join(c.localDir, "creds")

	// Determine whether we believe the creds exist already or not
	var exists bool
	if _, err := os.Stat(path); err == nil {
		exists = true
	} else {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
	}

	var creds map[string]string
	if exists {
		infraCtx.Ui.Message(
			"Cached and encrypted infrastructure credentials found.\n" +
				"Otto will now ask you for the password to decrypt these\n" +
				"credentials.\n\n")

		// If they exist, ask for the password
		value, err := infraCtx.Ui.Input(&ui.InputOpts{
			Id:          "creds_password",
			Query:       "Encrypted Credentials Password",
			Description: strings.TrimSpace(credsQueryPassExists),
		})
		if err != nil {
			return err
		}

		// If the password is not blank, then just read the credentials
		if value != "" {
			plaintext, err := cryptRead(path, value)
			if err == nil {
				err = json.Unmarshal(plaintext, &creds)
			}
			if err != nil {
				return fmt.Errorf(
					"error reading encrypted credentials: %s\n\n"+
						"If this error persists, you can force Otto to ask for credentials\n"+
						"again by inputting the empty password as the password.",
					err)
			}

			return nil
		}
	}

	// If we don't have creds, then we need to query the user via
	// the infrastructure implementation.
	if creds == nil {
		infraCtx.Ui.Message(
			"Existing infrastructure credentials were not found! Otto will\n" +
				"now ask you for infrastructure credentials. These will be encrypted\n" +
				"and saved on disk so this doesn't need to be repeated.\n\n" +
				"IMPORTANT: If you're re-entering new credentials, make sure the\n" +
				"credentials are for the same account, otherwise you may lose\n" +
				"access to your existing infrastructure Otto set up.\n\n")

		var err error
		creds, err = infra.Creds(infraCtx)
		if err != nil {
			return err
		}

		// Now that we have the credentials, we need to ask for the
		// password to encrypt and store them.
		var password string
		for password == "" {
			password, err = infraCtx.Ui.Input(&ui.InputOpts{
				Id:          "creds_password",
				Query:       "Password for Encrypting Credentials",
				Description: strings.TrimSpace(credsQueryPassNew),
			})
			if err != nil {
				return err
			}
		}

		// With the password, encrypt and write the data
		plaintext, err := json.Marshal(creds)
		if err != nil {
			// creds is a map[string]string, so this shouldn't ever fail
			panic(err)
		}

		if err := cryptWrite(path, password, plaintext); err != nil {
			return fmt.Errorf(
				"error writing encrypted credentials: %s", err)
		}
	}

	// Set the credentials
	infraCtx.InfraCreds = creds
	return nil
}

// Build builds the deployable artifact for the currently compiled
// Appfile.
func (c *Core) Build() error {
	// Get the infra implementation for this
	infra, infraCtx, err := c.infra()
	if err != nil {
		return err
	}
	if err := c.creds(infra, infraCtx); err != nil {
		return err
	}

	// We only use the root application for this task, upstream dependencies
	// don't have an effect on the build process.
	root, err := c.appfileCompiled.Graph.Root()
	if err != nil {
		return err
	}
	rootCtx, err := c.appContext(root.(*appfile.CompiledGraphVertex).File)
	if err != nil {
		return fmt.Errorf(
			"Error loading App: %s", err)
	}
	rootApp, err := c.app(rootCtx)
	if err != nil {
		return fmt.Errorf(
			"Error loading App: %s", err)
	}

	return rootApp.Build(rootCtx)
}

// Dev starts a dev environment for the current application. For destroying
// and other tasks against the dev environment, use the generic `Execute`
// method.
func (c *Core) Dev() error {
	// We need to get the root data separately since we need that for
	// all the function calls into the dependencies.
	root, err := c.appfileCompiled.Graph.Root()
	if err != nil {
		return err
	}
	rootCtx, err := c.appContext(root.(*appfile.CompiledGraphVertex).File)
	if err != nil {
		return fmt.Errorf(
			"Error loading App: %s", err)
	}
	rootApp, err := c.app(rootCtx)
	if err != nil {
		return fmt.Errorf(
			"Error loading App: %s", err)
	}

	// Go through all the dependencies and build their immutable
	// dev environment pieces for the final configuration.
	err = c.walk(func(appImpl app.App, ctx *app.Context, root bool) error {
		// If it is the root, we just return and do nothing else since
		// the root is a special case where we're building the actual
		// dev environment.
		if root {
			return nil
		}

		// Get the path to where we'd cache the dependency if we have
		// cached it...
		cachePath := filepath.Join(ctx.CacheDir, "dev-dep.json")

		// Check if we've cached this. If so, then use the cache.
		if _, err := app.ReadDevDep(cachePath); err == nil {
			ctx.Ui.Header(fmt.Sprintf(
				"Using cached dev dependency for '%s'",
				ctx.Appfile.Application.Name))
			return nil
		}

		// Build the development dependency
		dep, err := appImpl.DevDep(rootCtx, ctx)
		if err != nil {
			return fmt.Errorf(
				"Error building dependency for dev '%s': %s",
				ctx.Appfile.Application.Name,
				err)
		}

		// If we have a dependency with files, then verify the files
		// and store it in our cache directory so we can retrieve it
		// later.
		if len(dep.Files) > 0 {
			if err := dep.RelFiles(ctx.CacheDir); err != nil {
				return fmt.Errorf(
					"Error caching dependency for dev '%s': %s",
					ctx.Appfile.Application.Name,
					err)
			}

			if err := app.WriteDevDep(cachePath, dep); err != nil {
				return fmt.Errorf(
					"Error caching dependency for dev '%s': %s",
					ctx.Appfile.Application.Name,
					err)
			}
		}

		return nil
	})
	if err != nil {
		return err
	}

	// All the development dependencies are built/loaded. We now have
	// everything we need to build the complete development environment.
	return rootApp.Dev(rootCtx)
}

// Execute executes the given task for this Appfile.
func (c *Core) Execute(opts *ExecuteOpts) error {
	switch opts.Task {
	case ExecuteTaskDev:
		return c.executeApp(opts)
	case ExecuteTaskInfra:
		return c.executeInfra(opts)
	default:
		return fmt.Errorf("unknown task: %s", opts.Task)
	}
}

func (c *Core) executeApp(opts *ExecuteOpts) error {
	// Get the infra implementation for this
	appCtx, err := c.appContext(c.appfile)
	if err != nil {
		return err
	}
	app, err := c.app(appCtx)
	if err != nil {
		return err
	}

	// Set the action and action args
	appCtx.Action = opts.Action
	appCtx.ActionArgs = opts.Args

	// Build the infrastructure compilation context
	switch opts.Task {
	case ExecuteTaskDev:
		return app.Dev(appCtx)
	default:
		panic(fmt.Sprintf("uknown task: %s", opts.Task))
	}
}

func (c *Core) executeInfra(opts *ExecuteOpts) error {
	// Get the infra implementation for this
	infra, infraCtx, err := c.infra()
	if err != nil {
		return err
	}

	// Set the action and action args
	infraCtx.Action = opts.Action
	infraCtx.ActionArgs = opts.Args

	// Build the infrastructure compilation context
	return infra.Execute(infraCtx)
}

func (c *Core) appContext(f *appfile.File) (*app.Context, error) {
	// We need the configuration for the active infrastructure
	// so that we can build the tuple below
	config := f.ActiveInfrastructure()
	if config == nil {
		return nil, fmt.Errorf(
			"infrastructure not found in appfile: %s",
			f.Project.Infrastructure)
	}

	// The tuple we're looking for is the application type, the
	// infrastructure type, and the infrastructure flavor. Build that
	// tuple.
	tuple := app.Tuple{
		App:         f.Application.Type,
		Infra:       f.Project.Infrastructure,
		InfraFlavor: config.Flavor,
	}

	// The output directory for data. This is either the main app so
	// it goes directly into "app" or it is a dependency and goes into
	// a dep folder.
	outputDir := filepath.Join(c.compileDir, "app")
	if id := f.ID(); id != c.appfile.ID() {
		outputDir = filepath.Join(
			c.compileDir, fmt.Sprintf("dep-%s", id))
	}

	// The cache directory for this app
	cacheDir := filepath.Join(c.dataDir, "cache", f.ID())
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, fmt.Errorf(
			"error making cache directory '%s': %s",
			cacheDir, err)
	}

	return &app.Context{
		Dir:         outputDir,
		CacheDir:    cacheDir,
		Tuple:       tuple,
		Appfile:     f,
		Application: f.Application,
		Shared: context.Shared{
			Directory: c.dir,
			Ui:        c.ui,
		},
	}, nil
}

func (c *Core) app(ctx *app.Context) (app.App, error) {
	// Look for the app impl. factory
	f, ok := c.apps[ctx.Tuple]
	if !ok {
		return nil, fmt.Errorf(
			"app implementation for tuple not found: %s", ctx.Tuple)
	}

	// Start the impl.
	result, err := f()
	if err != nil {
		return nil, fmt.Errorf(
			"app failed to start properly: %s", err)
	}

	return result, nil
}

func (c *Core) infra() (infrastructure.Infrastructure, *infrastructure.Context, error) {
	// Get the infrastructure factory
	f, ok := c.infras[c.appfile.Project.Infrastructure]
	if !ok {
		return nil, nil, fmt.Errorf(
			"infrastructure type not supported: %s",
			c.appfile.Project.Infrastructure)
	}

	// Get the infrastructure configuration
	config := c.appfile.ActiveInfrastructure()
	if config == nil {
		return nil, nil, fmt.Errorf(
			"infrastructure not found in appfile: %s",
			c.appfile.Project.Infrastructure)
	}

	// Start the infrastructure implementation
	infra, err := f()
	if err != nil {
		return nil, nil, err
	}

	// The output directory for data
	outputDir := filepath.Join(
		c.compileDir, fmt.Sprintf("infra-%s", c.appfile.Project.Infrastructure))

	// Build the context
	return infra, &infrastructure.Context{
		Dir:   outputDir,
		Infra: config,
		Shared: context.Shared{
			Directory: c.dir,
			Ui:        c.ui,
		},
	}, nil
}

const credsQueryPassExists = `
Infrastructure credentials are required for this operation. Otto found
saved credentials that are password protected. Please enter the password
to decrypt these credentials. You may also just hit <enter> and leave
the password blank to force Otto to ask for the credentials again.
`

const credsQueryPassNew = `
This password will be used to encrypt and save the credentials so they
don't need to be repeated multiple times.
`
