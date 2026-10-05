package runtimeapi

import (
	"fmt"
	"sync"

	"github.com/ClickHouse/clickhouse-go/v2"
	mcpaccess "mcp-runtime/pkg/access"
	chpkg "mcp-runtime/pkg/clickhouse"
	"mcp-runtime/pkg/controlplane"
	"mcp-runtime/pkg/k8sclient"
	"mcp-runtime/pkg/platformstack"
)

// RuntimeServer composes runtime API dependencies for analytics, Kubernetes control-plane access, and platform identity.
type RuntimeServer struct {
	inventory     *InventoryService
	inventoryOnce sync.Once

	db         *chpkg.Client
	clickhouse clickhouse.Conn
	dbName     string
	apiKeys    map[string]struct{}
	identity   identityStore
	k8sClients *k8sclient.Clients
	control    *controlplane.Manager
	accessMgr  *mcpaccess.Manager
	stackMgr   *platformstack.Manager
	audit      auditWriter

	liveInventoryOnce  sync.Once
	liveInventoryCache *liveInventoryCache
	liveInventoryProbe liveInventoryProber
}

type DeploymentService struct {
	k8sClients *k8sclient.Clients
	identity   identityStore
	audit      auditWriter
}

type AccessService struct {
	k8sClients *k8sclient.Clients
	identity   identityStore
	accessMgr  *mcpaccess.Manager
	audit      auditWriter
}

type InventoryService struct {
	k8sClients         *k8sclient.Clients
	control            *controlplane.Manager
	access             *AccessService
	liveInventoryOnce  sync.Once
	liveInventoryCache *liveInventoryCache
	liveInventoryProbe liveInventoryProber
}

type RegistryPushService struct {
	k8sClients *k8sclient.Clients
}

// NewRuntimeServer creates a runtime server with Kubernetes access.
func NewRuntimeServer(db clickhouse.Conn, dbName string, apiKeys map[string]struct{}, identity identityStore) (*RuntimeServer, error) {
	// Create ClickHouse client wrapper
	chClient := &chpkg.Client{
		Conn:   db,
		DBName: dbName,
	}

	// Initialize Kubernetes clients (in-cluster or kubeconfig)
	k8sClients, err := k8sclient.New()
	if err != nil {
		// Log warning but don't fail - some endpoints will be unavailable
		fmt.Printf("[WARN] Kubernetes client initialization failed: %v\n", err)
		k8sClients = nil
	}

	var accessMgr *mcpaccess.Manager
	var stackMgr *platformstack.Manager
	var control *controlplane.Manager

	if k8sClients != nil {
		control = controlplane.New(k8sClients)
		accessMgr = mcpaccess.NewManager(k8sClients.Dynamic, k8sClients.Clientset)
		stackMgr = platformstack.NewManager(k8sClients.Clientset)
	}

	return &RuntimeServer{
		db:         chClient,
		clickhouse: db,
		dbName:     dbName,
		apiKeys:    apiKeys,
		identity:   identity,
		k8sClients: k8sClients,
		control:    control,
		accessMgr:  accessMgr,
		stackMgr:   stackMgr,
	}, nil
}

// Deployments returns the deployment capability owned by the runtime server.
func (s *RuntimeServer) Deployments() *DeploymentService {
	if s == nil {
		return nil
	}
	return &DeploymentService{
		k8sClients: s.k8sClients,
		identity:   s.identity,
		audit:      s.audit,
	}
}

// Access returns the grant/session capability owned by the runtime server.
func (s *RuntimeServer) Access() *AccessService {
	if s == nil {
		return nil
	}
	return &AccessService{
		k8sClients: s.k8sClients,
		identity:   s.identity,
		accessMgr:  s.accessMgr,
		audit:      s.audit,
	}
}

// Inventory returns the tool inventory capability owned by the runtime server.
func (s *RuntimeServer) Inventory() *InventoryService {
	if s == nil {
		return nil
	}
	s.inventoryOnce.Do(func() {
		s.inventory = &InventoryService{
			k8sClients:         s.k8sClients,
			control:            s.control,
			access:             s.Access(),
			liveInventoryProbe: s.liveInventoryProbe,
		}
	})
	return s.inventory
}

// RegistryPush returns the registry push transfer capability owned by the runtime server.
func (s *RuntimeServer) RegistryPush() *RegistryPushService {
	if s == nil {
		return nil
	}
	return &RegistryPushService{k8sClients: s.k8sClients}
}

func (s *RuntimeServer) controlPlane() *controlplane.Manager {
	if s.control != nil {
		return s.control
	}
	if s.k8sClients == nil {
		return nil
	}
	return controlplane.New(s.k8sClients)
}

// KubernetesAvailable reports whether Kubernetes-backed runtime endpoints can serve requests.
func (s *RuntimeServer) KubernetesAvailable() bool {
	return s != nil && s.k8sClients != nil
}

func (s *RuntimeServer) identityConfigured() bool {
	return s != nil && s.identity != nil && s.identity.Configured()
}
