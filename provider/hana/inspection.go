package hana

import (
	"context"
	"time"
)

// InspectionQueryTimeout bounds registration-time probe, sample, and
// metadata queries so a hanging query cannot stall provider creation
// indefinitely (audit P5-16).
const InspectionQueryTimeout = 30 * time.Second

// NewInspectionContext derives a probe context carrying
// InspectionQueryTimeout from parent. A nil parent means
// context.Background(); an earlier parent deadline (or its cancelation) is
// honored because the timeout context is a child of the parent.
func NewInspectionContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, InspectionQueryTimeout)
}
