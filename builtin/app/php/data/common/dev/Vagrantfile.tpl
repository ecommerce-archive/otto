# Generated by Otto, do not edit!
#
# This is the Vagrantfile generated by Otto for the development of
# this application/service. It should not be hand-edited. To modify the
# Vagrantfile, use the Appfile.

Vagrant.configure("2") do |config|
  config.vm.box = "hashicorp/precise64"

  # Host only network
  config.vm.network "private_network", ip: "{{ dev_ip_address }}"

  # Setup a synced folder from our working directory to /vagrant
  config.vm.synced_folder '{{ path.working }}', "/vagrant",
    owner: "vagrant", group: "vagrant"

  # Enable SSH agent forwarding so getting private dependencies works
  config.ssh.forward_agent = true

  # Foundation configuration (if any)
  {% for dir in foundation_dirs.dev %}
  dir = "/otto/foundation-{{ forloop.Counter }}"
  config.vm.synced_folder '{{ dir }}', dir
  config.vm.provision "shell", inline: "cd #{dir} && bash #{dir}/main.sh"
  {% endfor %}

  # Load all our fragments here for any dependencies.
  {% for fragment in dev_fragments %}
  {{ fragment|read }}
  {% endfor %}

  # Install build environment
  config.vm.provision "shell", inline: $script_app

  config.vm.provider :parallels do |p, o|
    o.vm.box = "parallels/ubuntu-12.04"
  end
end

$script_app = <<SCRIPT
#!/bin/bash

set -o nounset -o errexit -o pipefail -o errtrace

error() {
   local sourcefile=$1
   local lineno=$2
   echo "ERROR at ${sourcefile}:${lineno}; Last logs:"
   grep otto /var/log/syslog | tail -n 20
}
trap 'error "${BASH_SOURCE}" "${LINENO}"' ERR

oe() { "$@" 2>&1 | logger -t otto > /dev/null; }
ol() { echo "[otto] $@"; }

# Make it so that `vagrant ssh` goes directly to the correct dir
echo "cd /vagrant" >> /home/vagrant/.bashrc

# Configuring SSH for faster login
if ! grep "UseDNS no" /etc/ssh/sshd_config >/dev/null; then
  echo "UseDNS no" | sudo tee -a /etc/ssh/sshd_config >/dev/null
  oe sudo service ssh restart
fi

## NOTE:  Installing PHP packages requires different packages and
## order of install for different PHP versions.  We'll do a little 
## configuration here to ensure that we are installing the 
## proper PHP packages 

# Look for conditions based on the input PHP version. 

ol "Install PHP Version {{php_version}} "

php_version={{php_version}}


## UPDATE VERSION LIST:  Get current PHP versions
versionlist=()
updateVersionList(){
  versionlist=(`apt-cache show php5 | grep Version`)
  for i in "${versionlist[@]}"
    do
     if [[ $i == "Version"* ]]
   then
     versionlist=(${versionlist[@]/$i})
   fi
  done
}

# Update and add some packages for installing PPA files..
ol "Adding apt repositories and updating..."
export DEBIAN_FRONTEND=noninteractive
oe sudo apt-get update -y
oe sudo apt-get install -y python-software-properties software-properties-common apt-transport-https

oe sudo apt-get update -y
oe sudo apt-get install -y python-software-properties software-properties-common apt-transport-https

updateVersionList

latestVersion=${versionlist[0]}

## FUNCTION:  outputCurrentVersions
## PURPOSE:   A bit of debugging code if needed.
outputCurrentVersions()
{
  for i in "${versionlist[@]}"
  do
	ol $i
  done
}

## FUNCTION:  checkForVersion
## PURPOSE:   The idea here is to check to see if the version
##            we want to install is already available locally.
##            For the current Otto / Vagrant version (using only
##            Ubuntu 'precise', this won't matter as we'll be
##            installing PPAs for most PHP versions.  Testing
##            of the base script was done with a number of 
##            Ubuntu / Debian versions.
checkForVersion () 
{
	versionToCheck=$1
	echo "Check for Version ${versionToCheck} in ${versionlist}"
	# ASSUMPTION:  We'll check the version for now, but only 
	# the major version.  We'll rely on apt to install "only the latest".
    for i in "${versionlist[@]}"
    do
     if [[ $i == "${versionToCheck}"* ]]
     then
         return 1
     fi
    done    
    return 0
}

#update_result=`apt-get update`


checkForVersion $php_version
isVersionPresent=$?
ol "VERSION PRESENT: ${isVersionPresent}"

## PHP_VERSION NOTES:  Here we are performing different actions
## to install different PHP packages.  The 7.0 and HHVM installs
## for the current version of Otto will fail as we are using 
## the base Ubuntu precise distribution.
case $php_version in

 ## NOTE:  This will fail on the current box for Otto as 
 ## there is no 7.0 support for Ubuntu 12.04 (precise)
 ## The PPA itself is (at present) a little unstable.
 
 7.0) 
   if [[ $isVersionPresent == 1 ]]
    then
      echo "Install current version ${latestVersion}"
    else 
      echo "Install the 7.0 PPA"
      add-apt-repository -y ppa:ondrej/php-7.0 && apt-get update
      updateVersionList
      apt-get install -y --force-yes php5=${versionlist[0]}
    fi
 	;;
 ## NOTE:  HVVM installs will also fail on precise due to some
 ## missing libraries (libboost, etc.)
 HHVM) 
   if [[ $isVersionPresent == 1 ]]
    then
      ol "Install current version ${latestVersion}"
    else 
      ol "Install HHVM"
      apt-get install -y wget
      wget -O - http://dl.hhvm.com/conf/hhvm.gpg.key | apt-key add -
      echo deb http://dl.hhvm.com/ubuntu precise main | tee /etc/apt/sources.list.d/hhvm.list
      apt-get update && apt-get install -y --force-yes hhvm
    fi
 	;;


 5.6)
    if [[ $isVersionPresent == 1 ]]
    then
      ol "Install current version ${latestVersion}"
      oe apt-get install -y --force-yes php5=${latestVersion}
    else 
      ol "Install the 5.6 PPA"
      oe add-apt-repository -y ppa:ondrej/php5-5.6 && oe apt-get update
      updateVersionList
      oe sudo apt-get install -y --force-yes \
        php5=${versionlist[0]} \
        php5-mcrypt=${versionlist[0]} \
        php5-mysql=${versionlist[0]} \
        php5-fpm=${versionlist[0]} \
        php5-gd=${versionlist[0]} \
        php5-readline=${versionlist[0]} \
        php5-pgsql=${versionlist[0]} 
    fi
 	;;
 	
 5.5)
     if [[ $isVersionPresent == 1 ]]
    then
      ol "Install current version"
      apt-get install -y --force-yes php5=${versionlist[0]}
    else 
      ol "Install the 5.4 PPA"
      oe add-apt-repository -y ppa:ondrej/php5 && oe apt-get update
      updateVersionList
      oe sudo apt-get install -y --force-yes \
        php5=${versionlist[0]} \
        php5-mcrypt=${versionlist[0]} \
        php5-mysql=${versionlist[0]} \
        php5-fpm=${versionlist[0]} \
        php5-gd=${versionlist[0]} \
        php5-readline=${versionlist[0]} \
        php5-pgsql=${versionlist[0]} 
    fi
 	;;
 	
 5.4)
     if [[ $isVersionPresent == 1 ]]
    then
      ol "Install current version"
      apt-get install -y --force-yes php5=${versionlist[0]}
    else 
      ol "Install the 5.4 PPA"
      add-apt-repository -y ppa:ondrej/php5-oldstable && oe apt-get update
      updateVersionList
      oe sudo apt-get install -y --force-yes php5=${versionlist[0]}
    fi
 	;;
 *)
 	echo "Default"
esac


#ol "Installing PHP and supporting packages..."
oe sudo apt-get install -y \
  bzr git mercurial build-essential \
  curl

ol "Installing Composer..."
cd /tmp
curl -sS https://getcomposer.org/installer | php
oe sudo mv composer.phar /usr/local/bin/composer
SCRIPT
