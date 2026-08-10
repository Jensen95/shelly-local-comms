package mqttdisc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/Jensen95/shelly-local-comms/internal/app"
	"github.com/Jensen95/shelly-local-comms/internal/shelly"
)

// Service owns the broker connection for announce-discovery. Create it
// with NewService, run it with Run, and call RequestAnnounce any time to
// re-solicit announcements (e.g. from a UI refresh action).
type Service struct {
	settings app.MQTTSettings
	onDevice func(shelly.Device)

	mu       sync.Mutex
	listener *Listener
}

// NewService prepares announce-discovery against the broker in s.
func NewService(s app.MQTTSettings, onDevice func(shelly.Device)) *Service {
	return &Service{settings: s, onDevice: onDevice}
}

// RequestAnnounce broadcasts an announce request when connected; before
// the connection is up it is a no-op (Start broadcasts on connect
// anyway).
func (svc *Service) RequestAnnounce() {
	svc.mu.Lock()
	l := svc.listener
	svc.mu.Unlock()
	if l != nil {
		_ = l.RequestAnnounce()
	}
}

// Run connects to the broker and feeds announced devices to onDevice
// until ctx ends. Connection loss (and the initial connect) is retried in
// the background; subscriptions and the announce broadcast are
// re-established on every (re)connect.
func (svc *Service) Run(ctx context.Context) error {
	server := svc.settings.Server
	if !strings.Contains(server, "://") {
		server = "tcp://" + server
	}
	opts := mqtt.NewClientOptions().
		AddBroker(server).
		SetClientID("shellyctl-" + randomID()).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(10 * time.Second).
		// Handlers block (device enrichment over HTTP, config writes) and
		// publish back into the broker; with ordered dispatch paho runs
		// them inline in its single router goroutine, which stalls the
		// client and can deadlock on a full outbound queue. Unordered
		// dispatch runs each handler in its own goroutine.
		SetOrderMatters(false)
	if svc.settings.User != "" {
		opts.SetUsername(svc.settings.User)
	}
	if svc.settings.Password != "" {
		opts.SetPassword(svc.settings.Password)
	}

	opts.SetOnConnectHandler(func(mqtt.Client) {
		// Fires on every (re)connect; paho does not restore QoS0
		// subscriptions across reconnects on its own.
		svc.mu.Lock()
		l := svc.listener
		svc.mu.Unlock()
		if l != nil {
			_ = l.Start()
		}
	})

	client := mqtt.NewClient(opts)
	svc.mu.Lock()
	svc.listener = NewListener(&pahoBroker{c: client}, svc.onDevice,
		WithExtraPrefix(svc.settings.TopicPrefix))
	svc.mu.Unlock()
	client.Connect() // retried in the background per SetConnectRetry

	<-ctx.Done()
	client.Disconnect(250)
	svc.mu.Lock()
	svc.listener = nil
	svc.mu.Unlock()
	return ctx.Err()
}

type pahoBroker struct {
	c mqtt.Client
}

func (p *pahoBroker) Subscribe(topic string, cb func(topic string, payload []byte)) error {
	tok := p.c.Subscribe(topic, 0, func(_ mqtt.Client, msg mqtt.Message) {
		cb(msg.Topic(), msg.Payload())
	})
	tok.Wait()
	return tok.Error()
}

func (p *pahoBroker) Publish(topic string, payload []byte) error {
	tok := p.c.Publish(topic, 0, false, payload)
	tok.Wait()
	return tok.Error()
}

func randomID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "0000"
	}
	return hex.EncodeToString(b[:])
}
