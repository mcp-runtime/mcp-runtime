package runtimehandlers

import (
	"net/http"

	"mcp-runtime-api/internal/runtimeapi"
)

// HandleDashboardSummary routes the admin dashboard summary through the access service.
func HandleDashboardSummary(server *runtimeapi.RuntimeServer, w http.ResponseWriter, r *http.Request) {
	server.HandleDashboardSummary(w, r)
}

// HandleRuntimeServers routes runtime server collection requests through the access service.
func HandleRuntimeServers(server *runtimeapi.RuntimeServer, w http.ResponseWriter, r *http.Request) {
	server.HandleRuntimeServers(w, r)
}

// HandleRuntimeTools routes runtime tool catalog requests through the inventory service.
func HandleRuntimeTools(service *runtimeapi.InventoryService, w http.ResponseWriter, r *http.Request) {
	service.HandleRuntimeTools(w, r)
}

// HandleRuntimeServerItem routes runtime server item requests through the access service.
func HandleRuntimeServerItem(server *runtimeapi.RuntimeServer, w http.ResponseWriter, r *http.Request) {
	server.HandleRuntimeServerItem(w, r)
}

// HandleRuntimeServerEvents routes runtime server event requests through the access service.
func HandleRuntimeServerEvents(server *runtimeapi.RuntimeServer, w http.ResponseWriter, r *http.Request) {
	server.HandleRuntimeServerEvents(w, r)
}

// HandleRuntimeTeams routes runtime team collection requests through the access service.
func HandleRuntimeTeams(server *runtimeapi.RuntimeServer, w http.ResponseWriter, r *http.Request) {
	server.HandleRuntimeTeams(w, r)
}

// HandleRuntimeTeamItemPath routes runtime team item requests through the access service.
func HandleRuntimeTeamItemPath(server *runtimeapi.RuntimeServer, w http.ResponseWriter, r *http.Request) {
	server.HandleRuntimeTeamItemPath(w, r)
}

// HandleRuntimeNamespaces routes runtime namespace collection requests through the access service.
func HandleRuntimeNamespaces(server *runtimeapi.RuntimeServer, w http.ResponseWriter, r *http.Request) {
	server.HandleRuntimeNamespaces(w, r)
}

// HandleRuntimeNamespaceItem routes runtime namespace item requests through the access service.
func HandleRuntimeNamespaceItem(server *runtimeapi.RuntimeServer, w http.ResponseWriter, r *http.Request) {
	server.HandleRuntimeNamespaceItem(w, r)
}

// HandleRuntimeGrants routes runtime grant collection requests through the access service.
func HandleRuntimeGrants(service *runtimeapi.AccessService, w http.ResponseWriter, r *http.Request) {
	service.HandleRuntimeGrants(w, r)
}

// HandleRuntimeSessions routes runtime session collection requests through the access service.
func HandleRuntimeSessions(service *runtimeapi.AccessService, w http.ResponseWriter, r *http.Request) {
	service.HandleRuntimeSessions(w, r)
}

// HandleAdapterSession routes adapter session requests through the access service.
func HandleAdapterSession(service *runtimeapi.AccessService, w http.ResponseWriter, r *http.Request) {
	service.HandleAdapterSession(w, r)
}

// HandleAdapterPreflight reports adapter PKI readiness without creating a session.
func HandleAdapterPreflight(service *runtimeapi.AccessService, w http.ResponseWriter, r *http.Request) {
	service.HandleAdapterPreflight(w, r)
}

// HandleAdapterSessionItemPath deletes a still-pending session owned by the caller.
func HandleAdapterSessionItemPath(service *runtimeapi.AccessService, w http.ResponseWriter, r *http.Request) {
	service.HandleAdapterSessionItemPath(w, r)
}

// HandleAdapterCertificate routes adapter CSR enrollment requests.
func HandleAdapterCertificate(service *runtimeapi.AccessService, w http.ResponseWriter, r *http.Request) {
	service.HandleAdapterCertificate(w, r)
}

// HandleRuntimeRegistryPush routes registry push requests through the registry push service.
func HandleRuntimeRegistryPush(service *runtimeapi.RegistryPushService, w http.ResponseWriter, r *http.Request) {
	service.HandleRuntimeRegistryPush(w, r)
}

// HandleRuntimeComponents routes runtime component requests through the access service.
func HandleRuntimeComponents(server *runtimeapi.RuntimeServer, w http.ResponseWriter, r *http.Request) {
	server.HandleRuntimeComponents(w, r)
}

// HandleRuntimePolicy routes runtime policy requests through the access service.
func HandleRuntimePolicy(service *runtimeapi.AccessService, w http.ResponseWriter, r *http.Request) {
	service.HandleRuntimePolicy(w, r)
}

// HandleActionRestart routes restart actions through the access service.
func HandleActionRestart(server *runtimeapi.RuntimeServer, w http.ResponseWriter, r *http.Request) {
	server.HandleActionRestart(w, r)
}

// HandleGrantItemPath routes grant item requests through the access service.
func HandleGrantItemPath(service *runtimeapi.AccessService, w http.ResponseWriter, r *http.Request) {
	service.HandleGrantItemPath(w, r)
}

// HandleSessionItemPath routes session item requests through the access service.
func HandleSessionItemPath(service *runtimeapi.AccessService, w http.ResponseWriter, r *http.Request) {
	service.HandleSessionItemPath(w, r)
}
