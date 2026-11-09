package main

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/simonvetter/modbus"
)

const (
	maxTimeouts   = 3
	retryInterval = 2 * time.Second
)

type connection struct {
	client   *modbus.ModbusClient
	url      string
	timeouts int
}

func (c *connection) open(ctx context.Context, reqs <-chan holdingRequest) bool {
	for {
		err := c.client.Open()
		if err == nil {
			c.timeouts = 0
			log.Printf("connected to Modbus TCP device at %s (unit %d)", c.url, unitID)
			return true
		}
		log.Printf("connect to %s failed: %v, retrying in %s", c.url, err, retryInterval)
		if !waitRetry(ctx, retryInterval, reqs) {
			return false
		}
	}
}

func (c *connection) reconnect(ctx context.Context, reqs <-chan holdingRequest) bool {
	log.Printf("connection to %s lost, reconnecting", c.url)
	_ = c.client.Close()
	return c.open(ctx, reqs)
}

func (c *connection) lost(err error) bool {
	if err == nil {
		c.timeouts = 0
		return false
	}
	if errors.Is(err, modbus.ErrRequestTimedOut) {
		c.timeouts++
		return c.timeouts >= maxTimeouts
	}
	var me modbus.Error
	if errors.As(err, &me) {
		switch me {
		case modbus.ErrShortFrame, modbus.ErrProtocolError, modbus.ErrBadTransactionId, modbus.ErrUnknownProtocolId:
			return true
		}
		c.timeouts = 0
		return false
	}
	return true
}
