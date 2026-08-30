package main

import (
	"encoding/json"
	"github.com/JPSAUD501/CLIProxyAPI-Codex-Quota-Activation-Plugin/internal/quota"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var pluginService = quota.New(callHost)

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}
type registration struct {
	SchemaVersion uint32             `json:"schema_version"`
	Metadata      pluginapi.Metadata `json:"metadata"`
	Capabilities  struct {
		ManagementAPI bool `json:"management_api"`
	} `json:"capabilities"`
}
type rpcManagementRequest struct {
	pluginapi.ManagementRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}
type registrationResponse struct {
	Routes    []pluginapi.ManagementRoute `json:"routes,omitempty"`
	Resources []pluginapi.ResourceRoute   `json:"resources,omitempty"`
}

func handleMethod(method string, request []byte) ([]byte, bool) {
	value, err := dispatch(method, request)
	if err != nil {
		return errorEnvelope("plugin_error", err.Error(), 500, false), true
	}
	raw, err := okEnvelope(value)
	if err != nil {
		return errorEnvelope("encoding_error", err.Error(), 500, false), true
	}
	return raw, false
}
func dispatch(method string, request []byte) (any, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		var req lifecycleRequest
		if len(request) > 0 {
			if err := json.Unmarshal(request, &req); err != nil {
				return nil, err
			}
		}
		if err := pluginService.Configure(req.ConfigYAML); err != nil {
			return nil, err
		}
		return pluginRegistration(), nil
	case pluginabi.MethodManagementRegister:
		r := pluginService.Registration()
		return registrationResponse{Routes: r.Routes, Resources: r.Resources}, nil
	case pluginabi.MethodManagementHandle:
		var req rpcManagementRequest
		if err := json.Unmarshal(request, &req); err != nil {
			return nil, err
		}
		return pluginService.Management(req.ManagementRequest, req.HostCallbackID)
	default:
		return nil, stringError("unknown plugin method: " + method)
	}
}
func pluginRegistration() registration {
	r := registration{SchemaVersion: pluginabi.SchemaVersion, Metadata: pluginapi.Metadata{Name: "Codex Quota Activation", Version: quota.Version, Author: "JPSAUD501", GitHubRepository: "https://github.com/JPSAUD501/CLIProxyAPI-Codex-Quota-Activation-Plugin", ConfigFields: []pluginapi.ConfigField{{Name: "enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Enable Codex quota inspection."}, {Name: "auto_activate", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Automatically activate fresh eligible cycles."}, {Name: "scan_interval", Type: pluginapi.ConfigFieldTypeString, Description: "Automatic scan interval."}, {Name: "max_concurrency", Type: pluginapi.ConfigFieldTypeInteger, Description: "Global activation concurrency; v1 requires 1."}, {Name: "activation_model_mode", Type: pluginapi.ConfigFieldTypeString, Description: "Automatic model selection mode."}, {Name: "activation_model_override", Type: pluginapi.ConfigFieldTypeString, Description: "Optional recovery override."}}}}
	r.Capabilities.ManagementAPI = true
	return r
}
