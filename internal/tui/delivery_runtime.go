package tui

import "github.com/jacoelho/rvw/internal/delivery"

func (coordinator *Coordinator) runDeliveryWorker() {
	for job := range coordinator.deliveryJobs {
		coordinator.required.Wait(job.work.ID)
		payload, payloadErr := delivery.NewPayload(job.work.Prepared.Body())
		if payloadErr != nil {
			coordinator.complete(DeliveryOutcome{
				ID:  job.work.ID,
				Err: payloadErr,
			})
			continue
		}
		outcome, err := coordinator.delivery.Deliver(job.ctx, payload)
		coordinator.complete(DeliveryOutcome{
			ID:      job.work.ID,
			Outcome: outcome,
			Err:     err,
		})
	}
}
