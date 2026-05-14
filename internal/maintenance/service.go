package maintenance

import (
	"github.com/go-logr/logr"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"k8s.io/utils/clock"
)

// MaintenanceService encapsulates the core business logic for managing node maintenance operations.
type MaintenanceService struct {
	client   client.Client
	log      logr.Logger
	recorder record.EventRecorder
	clock clock.Clock
}

func NewMaintenanceService(client client.Client, log logr.Logger, recorder record.EventRecorder) *MaintenanceService {
	return &MaintenanceService{
		client:   client,
		log:      log.WithName("MaintenanceService"),
		recorder: recorder,
		clock: clock.RealClock{},
	}
}
