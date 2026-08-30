package main

/*
#include <stdint.h>
#include <stdlib.h>
typedef struct { void* ptr; size_t len; } cliproxy_buffer;
typedef int (*cliproxy_host_call_fn)(void*,const char*,const uint8_t*,size_t,cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*,size_t);
typedef struct { uint32_t abi_version; void* host_ctx; cliproxy_host_call_fn call; cliproxy_host_free_fn free_buffer; } cliproxy_host_api;
typedef int (*cliproxy_plugin_call_fn)(char*,uint8_t*,size_t,cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*,size_t); typedef void (*cliproxy_plugin_shutdown_fn)(void);
typedef struct { uint32_t abi_version; cliproxy_plugin_call_fn call; cliproxy_plugin_free_fn free_buffer; cliproxy_plugin_shutdown_fn shutdown; } cliproxy_plugin_api;
extern int cliproxyPluginCall(char*,uint8_t*,size_t,cliproxy_buffer*); extern void cliproxyPluginFree(void*,size_t); extern void cliproxyPluginShutdown(void);
static const cliproxy_host_api* stored_host; static void store_host_api(const cliproxy_host_api* host){stored_host=host;} static void clear_host_api(void){stored_host=NULL;}
static int call_host_api(const char* method,const uint8_t* request,size_t request_len,cliproxy_buffer* response){if(stored_host==NULL||stored_host->call==NULL)return 1;return stored_host->call(stored_host->host_ctx,method,request,request_len,response);}
static void free_host_buffer(void* ptr,size_t len){if(stored_host!=NULL&&stored_host->free_buffer!=NULL&&ptr!=NULL)stored_host->free_buffer(ptr,len);}
*/
import "C"
import (
	"encoding/json"
	"fmt"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"strings"
	"unsafe"
)

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if host == nil || plugin == nil || uint32(host.abi_version) != pluginabi.ABIVersion {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required", 400, false))
		return 1
	}
	var b []byte
	if request != nil && requestLen > 0 {
		b = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, failed := handleMethod(C.GoString(method), b)
	writeResponse(response, raw)
	if failed {
		return 1
	}
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() { pluginService.Shutdown(); C.clear_host_api() }
func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr != nil {
		response.ptr = ptr
		response.len = C.size_t(len(raw))
	}
}
func callHost(method string, payload any) (json.RawMessage, error) {
	request, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var cRequest unsafe.Pointer
	if len(request) > 0 {
		cRequest = C.CBytes(request)
		defer C.free(cRequest)
	}
	var response C.cliproxy_buffer
	rc := C.call_host_api(cMethod, (*C.uint8_t)(cRequest), C.size_t(len(request)), &response)
	var raw []byte
	if response.ptr != nil && response.len > 0 {
		raw = C.GoBytes(response.ptr, C.int(response.len))
	}
	if response.ptr != nil {
		C.free_host_buffer(response.ptr, response.len)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("host callback %s returned %d without response", method, int(rc))
	}
	var envelope pluginabi.Envelope
	if json.Unmarshal(raw, &envelope) != nil {
		return nil, errorsNew("invalid host response")
	}
	if !envelope.OK {
		if envelope.Error != nil && strings.TrimSpace(envelope.Error.Message) != "" {
			return nil, errorsNew(strings.TrimSpace(envelope.Error.Message))
		}
		return nil, errorsNew("host callback failed")
	}
	if rc != 0 {
		return nil, fmt.Errorf("host callback %s returned %d", method, int(rc))
	}
	return envelope.Result, nil
}

type stringError string

func (e stringError) Error() string { return string(e) }
func errorsNew(value string) error  { return stringError(value) }
func okEnvelope(value any) ([]byte, error) {
	result, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(pluginabi.Envelope{OK: true, Result: result})
}
func errorEnvelope(code, message string, status int, retryable bool) []byte {
	raw, _ := json.Marshal(pluginabi.Envelope{OK: false, Error: &pluginabi.Error{Code: code, Message: message, HTTPStatus: status, Retryable: retryable}})
	return raw
}
