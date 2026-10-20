package adapter

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

// ErrAttemptCancelled prevents a stopped attempt's late result from completing it.
var ErrAttemptCancelled = errors.New("artisan attempt is cancelled")

// ErrStopUnsupported means this platform cannot establish native worker ownership.
var ErrStopUnsupported = errors.New("identity-bound worker stopping is unsupported on this platform")

// WorkerStopper is the consumer boundary for stopping an owned worker. A nil
// error proves termination, not merely delivery of an interrupt. It does not
// delete work or mark the task finished.
type WorkerStopper interface {
	Stop(context.Context) error
}

type nativeWorker interface {
	stop(context.Context) error
	close() error
}

type workerHello struct {
	Identity
	Token   string `json:"token"`
	Result  string `json:"result"`
	PID     uint32 `json:"pid"`
	Created uint64 `json:"created"`
}

// OwnedWorker binds one cooperating local worker to one attempt. The worker
// must wait for acknowledgement before doing work. The private random token
// prevents accidental adoption; this is not a security boundary against other
// processes running as the same user. Calls are serialized by the consumer.
type OwnedWorker struct {
	spec     Spec
	exe      string
	token    string
	listener *net.TCPListener
	worker   nativeWorker
}

func NewOwnedWorker(spec Spec, exe string) (*OwnedWorker, error) {
	if !nativeStopSupported {
		return nil, ErrStopUnsupported
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if err := checkCancellation(spec.ResultFile, spec.Identity); err != nil {
		return nil, err
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, err
	}
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return nil, err
	}
	return &OwnedWorker{spec: spec, exe: exe, token: hex.EncodeToString(token[:]), listener: listener}, nil
}

func (o *OwnedWorker) Address() string { return o.listener.Addr().String() }
func (o *OwnedWorker) Token() string { return o.token }

// Accept verifies the worker identity and retains the native process handle
// before releasing the worker to do work. No PID is looked up again at stop.
func (o *OwnedWorker) Accept(ctx context.Context) error {
	if o.worker != nil {
		return errors.New("worker already bound")
	}
	cancel := context.AfterFunc(ctx, func() { _ = o.listener.Close() })
	defer cancel()
	conn, err := o.listener.AcceptTCP()
	if err != nil {
		return err
	}
	defer conn.Close()
	defer o.listener.Close()
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	closeConn := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer closeConn()
	var hello workerHello
	if err := json.NewDecoder(io.LimitReader(conn, 4096)).Decode(&hello); err != nil {
		return err
	}
	if hello.Token != o.token || hello.Identity != o.spec.Identity || hello.Result != o.spec.ResultFile {
		return errors.New("worker handshake does not belong to this attempt")
	}
	o.worker, err = openNativeWorker(hello.PID, hello.Created, o.exe)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err = io.WriteString(conn, "ready\n")
	return err
}

// Stop records cancellation before terminating and waiting on the retained
// handle. Even if termination fails, late success remains rejected, but the
// caller must not claim that the process stopped.
func (o *OwnedWorker) Stop(ctx context.Context) error {
	_ = o.listener.Close()
	if err := cancelAttempt(o.spec.ResultFile, o.spec.Identity); err != nil {
		return err
	}
	if o.worker == nil {
		return errors.New("worker ownership was not established; termination is unverified")
	}
	return o.worker.stop(ctx)
}

// Close releases resources only. It does not interrupt a worker.
func (o *OwnedWorker) Close() error {
	_ = o.listener.Close()
	if o.worker != nil {
		return o.worker.close()
	}
	return nil
}

// JoinOwnedWorker is called by the cooperating worker before any work or
// result publication. An unreachable owner or failed acknowledgement aborts
// the worker; it must never fall back to running without ownership.
func JoinOwnedWorker(ctx context.Context, address, token string, id Identity, result string) error {
	addr, err := net.ResolveTCPAddr("tcp4", address)
	if err != nil || addr == nil || !addr.IP.IsLoopback() {
		return errors.New("worker control address must be loopback IPv4")
	}
	created, err := nativeCreationTime()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp4", address)
	if err != nil {
		return err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	closeConn := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer closeConn()
	if err := json.NewEncoder(conn).Encode(workerHello{Identity: id, Token: token, Result: result, PID: uint32(os.Getpid()), Created: created}); err != nil {
		return err
	}
	var ack [6]byte
	if _, err := io.ReadFull(conn, ack[:]); err != nil {
		return err
	}
	if string(ack[:]) != "ready\n" {
		return errors.New("worker ownership was not acknowledged")
	}
	return checkCancellation(result, id)
}

func checkCancellation(path string, id Identity) error {
	data, err := os.ReadFile(path + ".cancelled")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var cancelled Identity
	if err := json.Unmarshal(data, &cancelled); err != nil {
		return fmt.Errorf("invalid attempt cancellation: %w", err)
	}
	if cancelled != id {
		return fmt.Errorf("cancellation sidecar: %w", ErrIdentityMismatch)
	}
	return ErrAttemptCancelled
}

func cancelAttempt(path string, id Identity) error {
	if err := checkCancellation(path, id); err != nil {
		if errors.Is(err, ErrAttemptCancelled) {
			return nil
		}
		return err
	}
	data, err := json.Marshal(id)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path+".cancelled", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}
