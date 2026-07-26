package coordinator

import (
	"testing"

	"github.com/jacoelho/rvw/internal/change"
)

func TestRequiredSchedulerAllowsAtMostEightBypasses(t *testing.T) {
	scheduler := NewScheduler()
	ids := make([]change.OperationID, 10)
	for index := range ids {
		ids[index], _ = change.NewOperationID(uint64(index + 1))
		scheduler.Register(ids[index])
	}
	starts := make([]<-chan struct{}, len(ids))
	for index := 1; index < len(ids); index++ {
		starts[index] = scheduler.markReady(ids[index])
	}
	for index := 1; index <= maxRequiredBypasses; index++ {
		select {
		case <-starts[index]:
		default:
			t.Fatalf("bypass %d was not granted", index)
		}
	}
	select {
	case <-starts[9]:
		t.Fatal("ninth bypass was granted before the oldest operation")
	default:
	}
	starts[0] = scheduler.markReady(ids[0])
	select {
	case <-starts[0]:
	default:
		t.Fatal("oldest ready operation was not granted")
	}
	select {
	case <-starts[9]:
	default:
		t.Fatal("blocked later operation did not run after the oldest")
	}
}
