package azuremonitor

import (
	"fmt"

	"github.com/grafana/grafana/pkg/components/simplejson"
	"github.com/grafana/grafana/pkg/setting"
)

const (
	AzureAuthManagedIdentity = "msi"
	AzureAuthClientSecret    = "clientsecret"
)

// Azure cloud names specific to Azure Monitor
const (
	azureMonitorPublic       = "azuremonitor"
	azureMonitorChina        = "chinaazuremonitor"
	azureMonitorUSGovernment = "govazuremonitor"
	azureMonitorGermany      = "germanyazuremonitor"
)

func getAuthType(cfg *setting.Cfg, pluginData *simplejson.Json) string {
	if authType := pluginData.Get("azureAuthType").MustString(); authType != "" {
		return authType
	} else {
		tenantId := pluginData.Get("tenantId").MustString()
		clientId := pluginData.Get("clientId").MustString()

		// If authentication type isn't explicitly specified and datasource has client credentials,
		// then this is existing datasource which is configured for app registration (client secret)
		if tenantId != "" && clientId != "" {
			return AzureAuthClientSecret
		}

		// For newly created datasource with no configuration, managed identity is the default authentication type
		// if they are enabled in Grafana config
		if cfg.Azure.ManagedIdentityEnabled {
			return AzureAuthManagedIdentity
		} else {
			return AzureAuthClientSecret
		}
	}
}

func getDefaultAzureCloud(cfg *setting.Cfg) (string, error) {
	switch cfg.Azure.Cloud {
	case setting.AzurePublic:
		return azureMonitorPublic, nil
	case setting.AzureChina:
		return azureMonitorChina, nil
	case setting.AzureUSGovernment:
		return azureMonitorUSGovernment, nil
	case setting.AzureGermany:
		return azureMonitorGermany, nil
	default:
		err := fmt.Errorf("the cloud '%s' not supported", cfg.Azure.Cloud)
		return "", err
	}
}

func getAzureCloud(cfg *setting.Cfg, pluginData *simplejson.Json) (string, error) {
	authType := getAuthType(cfg, pluginData)
	switch authType {
	case AzureAuthManagedIdentity:
		// In case of managed identity, the cloud is always same as where Grafana is hosted
		return getDefaultAzureCloud(cfg)
	case AzureAuthClientSecret:
		if cloud := pluginData.Get("cloudName").MustString(); cloud != "" {
			return cloud, nil
		} else {
			return getDefaultAzureCloud(cfg)
		}
	default:
		err := fmt.Errorf("the authentication type '%s' not supported", authType)
		return "", err
	}
}
