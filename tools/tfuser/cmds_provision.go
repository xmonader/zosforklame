package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"time"

	"github.com/pkg/errors"
	"github.com/threefoldtech/zosv2/modules"
	"github.com/threefoldtech/zosv2/modules/identity"
	"github.com/threefoldtech/zosv2/modules/provision"

	"github.com/urfave/cli"
)

var (
	day             = time.Hour * 24
	defaultDuration = day * 30
)

func cmdsProvision(c *cli.Context) error {
	var (
		schema   []byte
		path     = c.String("schema")
		nodeIDs  = c.StringSlice("node")
		seedPath = c.String("seed")
		duration = time.Duration(c.Int64("duration"))
		err      error
	)

	if duration == 0 {
		duration = defaultDuration
	} else {
		duration = duration * day
	}

	keypair, err := identity.LoadSeed(seedPath)
	if err != nil {
		return err
	}

	if path == "-" {
		schema, err = ioutil.ReadAll(os.Stdin)
	} else {
		schema, err = ioutil.ReadFile(path)
	}
	if err != nil {
		return err
	}

	r := provision.Reservation{}
	if err := json.Unmarshal(schema, &r); err != nil {
		return err
	}

	r.Duration = duration
	r.Created = time.Now()

	// set the user ID into the reservation schema
	r.User = keypair.Identity()

	if err := r.Sign(keypair.PrivateKey); err != nil {
		return errors.Wrap(err, "failed to sign the reservation")
	}

	if err := output(path, r); err != nil {
		return err
	}

	for _, nodeID := range nodeIDs {
		if err := store.Reserve(r, modules.StrIdentifier(nodeID)); err != nil {
			return err
		}
		fmt.Printf("reservation send for node %s\n", nodeID)
	}

	return nil
}

func embed(schema interface{}, t provision.ReservationType) (*provision.Reservation, error) {
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}

	r := &provision.Reservation{
		Type: t,
		Data: raw,
	}

	return r, nil
}
