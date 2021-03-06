/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package config

import (
	"fmt"
	"io/ioutil"
	"net"

	"github.com/golang/glog"

	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	netutil "k8s.io/apimachinery/pkg/util/net"
	bootstraputil "k8s.io/client-go/tools/bootstrap/token/util"
	kubeadmapi "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm"
	kubeadmscheme "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm/scheme"
	kubeadmapiv1alpha3 "k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm/v1alpha3"
	"k8s.io/kubernetes/cmd/kubeadm/app/apis/kubeadm/validation"
	kubeadmconstants "k8s.io/kubernetes/cmd/kubeadm/app/constants"
	nodeutil "k8s.io/kubernetes/pkg/util/node"
)

// SetInitDynamicDefaults checks and sets configuration values for the MasterConfiguration object
func SetInitDynamicDefaults(cfg *kubeadmapi.MasterConfiguration) error {

	// validate cfg.API.AdvertiseAddress.
	addressIP := net.ParseIP(cfg.API.AdvertiseAddress)
	if addressIP == nil && cfg.API.AdvertiseAddress != "" {
		return fmt.Errorf("couldn't use \"%s\" as \"apiserver-advertise-address\", must be ipv4 or ipv6 address", cfg.API.AdvertiseAddress)
	}
	// Choose the right address for the API Server to advertise. If the advertise address is localhost or 0.0.0.0, the default interface's IP address is used
	// This is the same logic as the API Server uses
	ip, err := netutil.ChooseBindAddress(addressIP)
	if err != nil {
		return err
	}
	cfg.API.AdvertiseAddress = ip.String()
	ip = net.ParseIP(cfg.API.AdvertiseAddress)
	if ip.To4() != nil {
		cfg.KubeProxy.Config.BindAddress = kubeadmapiv1alpha3.DefaultProxyBindAddressv4
	} else {
		cfg.KubeProxy.Config.BindAddress = kubeadmapiv1alpha3.DefaultProxyBindAddressv6
	}
	// Resolve possible version labels and validate version string
	if err := NormalizeKubernetesVersion(cfg); err != nil {
		return err
	}

	// Downcase SANs. Some domain names (like ELBs) have capitals in them.
	LowercaseSANs(cfg.APIServerCertSANs)

	// Populate the .Token field with a random value if unset
	// We do this at this layer, and not the API defaulting layer
	// because of possible security concerns, and more practically
	// because we can't return errors in the API object defaulting
	// process but here we can.
	for i, bt := range cfg.BootstrapTokens {
		if bt.Token != nil && len(bt.Token.String()) > 0 {
			continue
		}

		tokenStr, err := bootstraputil.GenerateBootstrapToken()
		if err != nil {
			return fmt.Errorf("couldn't generate random token: %v", err)
		}
		token, err := kubeadmapi.NewBootstrapTokenString(tokenStr)
		if err != nil {
			return err
		}
		cfg.BootstrapTokens[i].Token = token
	}

	cfg.NodeRegistration.Name = nodeutil.GetHostname(cfg.NodeRegistration.Name)

	// Only if the slice is nil, we should append the master taint. This allows the user to specify an empty slice for no default master taint
	if cfg.NodeRegistration.Taints == nil {
		cfg.NodeRegistration.Taints = []v1.Taint{kubeadmconstants.MasterTaint}
	}

	return nil
}

// ConfigFileAndDefaultsToInternalConfig takes a path to a config file and a versioned configuration that can serve as the default config
// If cfgPath is specified, defaultversionedcfg will always get overridden. Otherwise, the default config (often populated by flags) will be used.
// Then the external, versioned configuration is defaulted and converted to the internal type.
// Right thereafter, the configuration is defaulted again with dynamic values (like IP addresses of a machine, etc)
// Lastly, the internal config is validated and returned.
func ConfigFileAndDefaultsToInternalConfig(cfgPath string, defaultversionedcfg *kubeadmapiv1alpha3.MasterConfiguration) (*kubeadmapi.MasterConfiguration, error) {
	internalcfg := &kubeadmapi.MasterConfiguration{}

	if cfgPath != "" {
		// Loads configuration from config file, if provided
		// Nb. --config overrides command line flags
		glog.V(1).Infoln("loading configuration from the given file")

		b, err := ioutil.ReadFile(cfgPath)
		if err != nil {
			return nil, fmt.Errorf("unable to read config from %q [%v]", cfgPath, err)
		}
		return BytesToInternalConfig(b)
	}

	// Takes passed flags into account; the defaulting is executed once again enforcing assignement of
	// static default values to cfg only for values not provided with flags
	kubeadmscheme.Scheme.Default(defaultversionedcfg)
	kubeadmscheme.Scheme.Convert(defaultversionedcfg, internalcfg, nil)

	return defaultAndValidate(internalcfg)
}

// BytesToInternalConfig converts a byte array to an internal, defaulted and validated configuration object
func BytesToInternalConfig(b []byte) (*kubeadmapi.MasterConfiguration, error) {
	internalcfg := &kubeadmapi.MasterConfiguration{}

	if err := DetectUnsupportedVersion(b); err != nil {
		return nil, err
	}

	if err := runtime.DecodeInto(kubeadmscheme.Codecs.UniversalDecoder(), b, internalcfg); err != nil {
		return nil, err
	}

	return defaultAndValidate(internalcfg)
}

func defaultAndValidate(cfg *kubeadmapi.MasterConfiguration) (*kubeadmapi.MasterConfiguration, error) {
	// Applies dynamic defaults to settings not provided with flags
	if err := SetInitDynamicDefaults(cfg); err != nil {
		return nil, err
	}
	// Validates cfg (flags/configs + defaults + dynamic defaults)
	if err := validation.ValidateMasterConfiguration(cfg).ToAggregate(); err != nil {
		return nil, err
	}
	return cfg, nil
}
