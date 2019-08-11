// Package provision is a daemon that pulls
// on reservation source, and then tries to
// apply these reservations locally.
// Note that, provision module doesn't expose
// any interface on zbus. since it should not
// be driven by users, instead all reservation
// should be pushed by the reservation source.
package provision

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/pkg/errors"
	"github.com/rs/zerolog/log"
)

// ReservationType type
type ReservationType string

const (

	// ContainerReservation type
	ContainerReservation ReservationType = "container"
	// VolumeReservation type
	VolumeReservation ReservationType = "volume"
	// NetworkReservation type
	NetworkReservation ReservationType = "network"
)

// ReplyTo defines how report the result of the provisioning operation
type ReplyTo string

// Reservation struct
type Reservation struct {
	// ID of the reservation
	ID string `json:"id"`
	// Identification of the user requesting the reservation
	User string `json:"user_id"`
	// Type of the reservation (container, zdb, vm, etc...)
	Type ReservationType `json:"type"`
	// Data is the reservation type arguments.
	Data json.RawMessage `json:"data"`
	// Signature is the signature to the reservation
	// it contains all the field of this struct except the signature itself
	Signature []byte `json:"signature"`
	// Date of creation
	Created time.Time `json:"created"`
	// Duration of the reservation
	Duration time.Duration `json:"duration"`
}

func (r Reservation) validate() error {
	if err := Verify(r); err != nil {
		log.Warn().
			Err(err).
			Str("id", string(r.ID)).
			Msg("verification of reservation signature failed")
		return errors.Wrapf(err, "verification of reservation %s signature failed", r.ID)
	}

	if r.Duration <= 0 {
		return fmt.Errorf("reservation %s has not duration", r.ID)
	}

	if r.Created.IsZero() {
		return fmt.Errorf("wrong creation date in reservation %s", r.ID)
	}

	if isExpired(&r) {
		return fmt.Errorf("reservation %s has expired", r.ID)
	}

	return nil
}

// ReservationSource interface. The source
// defines how the node will get reservation requests
// then reservations are applied to the node to deploy
// a resource of the given Reservation.Type
type ReservationSource interface {
	Reservations(ctx context.Context) <-chan Reservation
}

// Engine interface
type Engine interface {
	Run(ctx context.Context) error
}

type provisioner func(ctx context.Context, reservation Reservation) (interface{}, error)
type decomissioner func(ctx context.Context, reservation Reservation) error

var (
	// provisioners defines the entry point for the different
	// reservation provisioners. Currently only containers are
	// supported.
	provisioners = map[ReservationType]provisioner{
		ContainerReservation: containerProvision,
		VolumeReservation:    volumeProvision,
		NetworkReservation:   networkProvision,
	}

	decomissioners = map[ReservationType]decomissioner{
		ContainerReservation: containerDecomission,
		VolumeReservation:    volumeDecomission,
		NetworkReservation:   networkDecomission,
	}
)
