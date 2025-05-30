package miniod

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/canonical/lxd/lxd/locking"
	"github.com/canonical/lxd/lxd/operations"
	"github.com/canonical/lxd/lxd/state"
	storageDrivers "github.com/canonical/lxd/lxd/storage/drivers"
	"github.com/canonical/lxd/shared"
	"github.com/canonical/lxd/shared/api"
	"github.com/canonical/lxd/shared/cancel"
	"github.com/canonical/lxd/shared/logger"
)

// minioHost is the host address that the local MinIO processes will listen on.
const minioHost = "127.0.0.1"

// minioLockPrefix is the prefix used for per-bucket MinIO spawn lock.
const minioLockPrefix = "minio_"

// minioAdminUser in MinIO.
const minioAdminUser = "lxd-admin"

// minioBucketDir the directory on the storage volume used for the MinIO bucket.
const minioBucketDir = "minio"

// Process represents a running minio process.
type Process struct {
	bucketName   string
	transactions uint
	url          url.URL
	consoleURL   url.URL
	username     string
	password     string
	cancel       cancel.Canceller
	err          error
}

// URL of MinIO process.
func (p *Process) URL() url.URL {
	return p.url
}

// AdminUser returns admin user name.
func (p *Process) AdminUser() string {
	return p.username
}

// AdminClient returns admin client for the minio process.
func (p *Process) AdminClient() (*minioAdmin, error) {
	adminClient, err := NewAdminClient(p.url.Host, p.username, p.password)
	if err != nil {
		return nil, err
	}

	return adminClient, nil
}

// S3Client returns S3 client for the minio process.
func (p *Process) S3Client() (*minio.Client, error) {
	s3Client, err := minio.New(p.url.Host, &minio.Options{
		Creds:  credentials.NewStaticV4(p.username, p.password, ""),
		Secure: false,
	})
	if err != nil {
		return nil, err
	}

	return s3Client, nil
}

// Stop will try and cleanly stop the service and if context is cancelled then it forcefully kills the process.
// If ctx doesn't have a deadline then a default timeout of 5s is added.
func (p *Process) Stop(ctx context.Context) error {
	err := p.cancel.Err()
	if err != nil {
		return nil
	}

	spawnUnlock, err := locking.Lock(context.TODO(), minioLockPrefix+p.bucketName)
	if err != nil {
		return err
	}

	defer spawnUnlock()

	defer p.cancel.Cancel()

	_, ok := ctx.Deadline()
	if !ok {
		// Set default timeout of 5s if no deadline context provided.
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(5*time.Second))
		defer cancel()
	}

	adminClient, err := p.AdminClient()
	if err != nil {
		return err
	}

	err = adminClient.ServiceStop(ctx)
	if err != nil {
		return err
	}

	select {
	case <-ctx.Done(): // Clean stop timed out.
	case <-p.cancel.Done(): // Process has stopped.
	}

	return nil
}

// WaitReady waits until process is ready.
func (p *Process) WaitReady(ctx context.Context) error {
	var lastErr error
	for {
		select {
		case <-ctx.Done():
			err := fmt.Errorf("Failed to wait for MinIO server process: %w", ctx.Err())
			if lastErr != nil {
				err = fmt.Errorf("%w: %w", lastErr, err)
				p.cancel.Cancel()
				if p.err != nil {
					err = fmt.Errorf("%w: %w", p.err, err)
				}
			}

			return err
		default:
			adminClient, err := p.AdminClient()
			if adminClient != nil {
				_, err = adminClient.GetConfig(ctx)
				if err == nil {
					return nil
				}
			}

			lastErr = err
			time.Sleep(time.Millisecond * 100)
		}
	}
}

var miniosMu sync.Mutex
var minios = make(map[string]*Process)

// EnsureRunning starts a MinIO process for the bucket (if not already running) and returns running Process.
func EnsureRunning(s *state.State, bucketVol storageDrivers.Volume) (*Process, error) {
	bucketName := bucketVol.Name()

	// Prevent concurrent spawning of same bucket.
	spawnUnlock, err := locking.Lock(context.TODO(), minioLockPrefix+bucketName)
	if err != nil {
		return nil, err
	}

	defer spawnUnlock()

	// Check if there is an existing running minio process for the bucket, and if so return it.
	miniosMu.Lock()
	minioProc, found := minios[bucketName]
	if found {
		// Increment transaction counter to keep process alive.
		minioProc.transactions++
		minios[bucketName] = minioProc
		miniosMu.Unlock()

		return minioProc, nil
	}

	miniosMu.Unlock()

	// Find free random port for minio process to listen on.
	listener1, err := net.Listen("tcp", minioHost+":0")
	if err != nil {
		return nil, fmt.Errorf("Failed finding free listen port for bucket MinIO process: %w", err)
	}

	listener2, err := net.Listen("tcp", minioHost+":0")
	if err != nil {
		return nil, fmt.Errorf("Failed finding free listen port for bucket MinIO process: %w", err)
	}

	listenPort := strconv.Itoa(listener1.Addr().(*net.TCPAddr).Port)
	consolePort := strconv.Itoa(listener2.Addr().(*net.TCPAddr).Port)

	err = listener1.Close()
	if err != nil {
		return nil, err
	}

	err = listener2.Close()
	if err != nil {
		return nil, err
	}

	minioProc = &Process{
		bucketName:   bucketName,
		transactions: 1,
		url:          api.NewURL().Scheme("http").Host(net.JoinHostPort(minioHost, listenPort)).URL,
		consoleURL:   api.NewURL().Scheme("http").Host(net.JoinHostPort(minioHost, consolePort)).URL,
		username:     minioAdminUser,      // Persistent admin user required to keep config between restarts.
		password:     uuid.New().String(), // Random admin password for service.
		cancel:       cancel.New(),
	}

	miniosMu.Lock()
	minios[bucketName] = minioProc
	miniosMu.Unlock()

	env := append(os.Environ(),
		"MINIO_BROWSER=off",
		"MINIO_ROOT_USER="+minioProc.username,
		"MINIO_ROOT_PASSWORD="+minioProc.password,
	)

	bucketPath := filepath.Join(bucketVol.MountPath(), minioBucketDir)

	args := []string{
		"server",
		bucketPath,
		"--address", minioProc.url.Host,
		"--console-address", minioProc.consoleURL.Host,
	}

	l := logger.AddContext(logger.Ctx{"bucketName": bucketName, "bucketPath": bucketPath, "listenPort": listenPort})

	// Launch minio process in background.
	go func() {
		err := bucketVol.MountTask(func(mountPath string, op *operations.Operation) error {
			l.Debug("MinIO bucket starting")

			newDirMode := os.ModeDir | os.FileMode(0700)

			if !shared.PathExists(bucketPath) {
				err = os.Mkdir(bucketPath, newDirMode)
				if err != nil {
					return fmt.Errorf("Failed creating MinIO bucket directory %q: %w", bucketPath, err)
				}
			}

			dirInfo, err := os.Lstat(bucketPath)
			if err != nil {
				return fmt.Errorf("Failed getting MinIO bucket directory info %q: %w", bucketPath, err)
			}

			dirMode, dirUID, dirGID := shared.GetOwnerMode(dirInfo)

			// Ensure file ownership is correct.
			if uint32(dirUID) != s.OS.UnprivUID || uint32(dirGID) != s.OS.UnprivGID {
				l.Debug("Setting MinIO bucket ownership", logger.Ctx{"currentOwner": dirUID, "currentGroup": dirGID, "newOwner": s.OS.UnprivUID, "newGroup": s.OS.UnprivGID})
				err = filepath.Walk(bucketPath, func(path string, _ os.FileInfo, err error) error {
					if err != nil {
						return err
					}

					err = os.Chown(path, int(s.OS.UnprivUID), int(s.OS.UnprivGID))
					if err != nil {
						return fmt.Errorf("Failed setting ownership on MinIO bucket file %q: %w", path, err)
					}

					return nil
				})
				if err != nil {
					return err
				}
			}

			// Ensure permissions are correct.
			if dirMode != newDirMode {
				l.Debug("Setting MinIO bucket permissions", logger.Ctx{"currentMode": dirMode, "newMode": newDirMode})
				err = os.Chmod(bucketPath, newDirMode)
				if err != nil {
					return fmt.Errorf("Failed setting permissions on MinIO bucket directory %q: %w", bucketPath, err)
				}
			}

			cmd := exec.CommandContext(minioProc.cancel, "minio", args...)
			cmd.Env = env

			// Drop privileges.
			cmd.SysProcAttr = &syscall.SysProcAttr{
				Credential: &syscall.Credential{
					Uid: s.OS.UnprivUID,
					Gid: s.OS.UnprivGID,
				},
			}

			// Capture stderr.
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			minioProc.err = cmd.Run()
			if minioProc.err != nil && minioProc.cancel.Err() == nil {
				l.Error("Failed starting MinIO bucket", logger.Ctx{"err": minioProc.err, "stdErr": stderr.String(), "stdout": stdout.String()})
			} else {
				l.Debug("MinIO bucket stopped")
			}

			return nil
		}, nil)
		if err != nil {
			l.Error("Failed mounting bucket volume", logger.Ctx{"err": err})
		}

		// Delete process entry once the process has stopped or failed to start.
		minioProc.cancel.Cancel()

		miniosMu.Lock()
		delete(minios, bucketName)
		miniosMu.Unlock()
	}()

	// Wait up to 10s for service to become ready. Pass the minioProc.cancel as parent context so that if the
	// minio process fails to start then this context will immediately be cancelled.
	waitReadyCtx, waitReadyCtxCancel := context.WithTimeout(minioProc.cancel, time.Second*10)
	defer waitReadyCtxCancel()

	err = minioProc.WaitReady(waitReadyCtx)
	if err != nil {
		return nil, fmt.Errorf("Failed connecting to bucket: %w", err)
	}

	l.Debug("MinIO bucket ready")

	// Launch go routine for idle process cleanup.
	go func() {
		var lastTransactionCount uint

		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if lastTransactionCount == minioProc.transactions {
					// No transactions since last loop, stop the service.
					l.Debug("Stopping MinIO bucket due to inactivity")
					_ = minioProc.Stop(context.Background())
					return
				}

				lastTransactionCount = minioProc.transactions
			case <-minioProc.cancel.Done():
				return
			}
		}
	}()

	return minioProc, nil
}

// Get returns an existing MinIO process if it exists.
func Get(bucketName string) (*Process, error) {
	// Wait for any ongoing spawn of the bucket process to finish.
	spawnUnlock, err := locking.Lock(context.TODO(), minioLockPrefix+bucketName)
	if err != nil {
		return nil, err
	}

	defer spawnUnlock()

	// Check if there is an existing running minio process for the bucket, and if so return it.
	miniosMu.Lock()
	defer miniosMu.Unlock()

	minioProc, found := minios[bucketName]
	if found {
		// Increment transaction counter to keep process alive.
		minioProc.transactions++
		minios[bucketName] = minioProc

		return minioProc, nil
	}

	return nil, nil
}

// StopAll stops all MinIO processes cleanly.
func StopAll() {
	miniosMu.Lock()
	minioProcs := make([]*Process, 0, len(minios))
	for _, m := range minios {
		minioProc := m
		minioProcs = append(minioProcs, minioProc)
	}

	miniosMu.Unlock()

	if len(minioProcs) > 0 {
		logger.Info("Stopping MinIO processes")
		for _, minioProc := range minios {
			_ = minioProc.Stop(context.Background())
		}
	}
}
