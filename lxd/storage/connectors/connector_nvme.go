package connectors

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/canonical/lxd/lxd/util"
	"github.com/canonical/lxd/shared"
	"github.com/canonical/lxd/shared/logger"
	"github.com/canonical/lxd/shared/revert"
)

var _ Connector = &connectorNVMe{}

type connectorNVMe struct {
	common
}

// Type returns the type of the connector.
func (c *connectorNVMe) Type() string {
	return TypeNVME
}

// Version returns the version of the NVMe CLI.
func (c *connectorNVMe) Version() (string, error) {
	logger.Debugf("NVMe Version()")

	// Detect and record the version of the NVMe CLI.
	out, err := shared.RunCommandContext(context.Background(), "nvme", "version")
	if err != nil {
		return "", fmt.Errorf("Failed to get nvme-cli version: %w", err)
	}

	fields := strings.Split(strings.TrimSpace(out), " ")
	if strings.HasPrefix(out, "nvme version ") && len(fields) > 2 {
		return fields[2] + " (nvme-cli)", nil
	}

	return "", fmt.Errorf("Failed to get nvme-cli version: Unexpected output %q", out)
}

// LoadModules loads the NVMe/TCP kernel modules.
// Returns true if the modules can be loaded.
func (c *connectorNVMe) LoadModules() error {
	logger.Debugf("NVMe LoadModules()")

	err := util.LoadModule("nvme_fabrics")
	if err != nil {
		return err
	}

	return util.LoadModule("nvme_tcp")
}

// QualifiedName returns a custom NQN generated from the server UUID.
// Getting the NQN from /etc/nvme/hostnqn would require the nvme-cli
// package to be installed on the host.
func (c *connectorNVMe) QualifiedName() (string, error) {
	return "nqn.2014-08.org.nvmexpress:uuid:" + c.serverUUID, nil
}

// Function to extract subnqn from the output of nvme discover.
func extractUniqueSubNQN(output string) (string, error) {
	logger.Debugf("NVMe extractUniqueSubNQN()")

	// Use regex to find subnqn entries in the output
	re := regexp.MustCompile(`subnqn:\s*([^\n]+)`)
	matches := re.FindAllStringSubmatch(output, -1)

	if len(matches) == 0 {
		return "", fmt.Errorf("no subnqn found in output")
	}

	// Assume the first match is the expected consistent subnqn
	expectedSubnqn := strings.TrimSpace(matches[0][1])
	for _, match := range matches {
		if len(match) > 1 {
			currentSubnqn := strings.TrimSpace(match[1])
			if currentSubnqn != expectedSubnqn {
				return "", fmt.Errorf("inconsistent subnqn values found: %s and %s", expectedSubnqn, currentSubnqn)
			}
		}
	}

	return expectedSubnqn, nil
}

// perform NVMe discovery and extract subnqn.
func discoverNVMeTCP(ctx context.Context, targetAddr string) (string, error) {
	logger.Debugf("NVMe discoverNVMeTCP()")

	command := []string{"nvme", "discover", "--transport", "tcp", "--traddr", targetAddr, "-s", "4420"}
	logger.Debugf("NVMe running command:\n%s", strings.Join(command, " "))

	stdout, err := shared.RunCommandContext(ctx, command[0], command[1:]...)
	logger.Debugf("NVMe discover output:\n%s", stdout)
	if err != nil {
		return "", fmt.Errorf("Failed to discover NVMe/TCP target %s: %w", targetAddr, err)
	}

	// Extract subnqn from output
	subnqn, err := extractUniqueSubNQN(stdout)
	if err != nil {
		return "", err
	}

	return subnqn, nil
}

// Connect establishes a connection with the target on the given address.
func (c *connectorNVMe) Connect(ctx context.Context, targetQN string, targetAddresses ...string) (revert.Hook, error) {
	logger.Debugf("NVMe Connect()")

	// Connects to the provided target address, if the connection is not yet established.
	connectFunc := func(ctx context.Context, session *session, targetAddr string) error {
		if session != nil && slices.Contains(session.addresses, targetAddr) {
			// Already connected.
			return nil
		}

		hostNQN, err := c.QualifiedName()
		if err != nil {
			return err
		}

		var waitSeconds = 15
		logger.Debugf("NVMe coonnect timeout in seconds: %d", waitSeconds)
		time.Sleep(time.Duration(waitSeconds) * time.Second)

		subnqn, err := discoverNVMeTCP(ctx, targetAddr)
		if err != nil {
			return fmt.Errorf("Failed to discover NVMe/TCP target %s: %w", targetAddr, err)
		}
		logger.Debugf("NVMe discovered SubNQN: %s", subnqn)

		targetQN = subnqn

		if subnqn != "" && subnqn != targetQN {
			logger.Debugf("NVMe temp fix! Override hpe.target.nqn %q wit SubNQN %q", targetQN, subnqn)
			targetQN = subnqn
		}

		command := []string{"nvme", "connect", "--transport", "tcp", "--traddr", targetAddr, "--nqn", targetQN, "--hostnqn", hostNQN, "--hostid", c.serverUUID, "-s", "4420"}
		logger.Debugf("NVMe running command:\n%s", strings.Join(command, " "))

		nvmeConnect, err := shared.RunCommandContext(ctx, command[0], command[1:]...)
		if err != nil {
			return fmt.Errorf("NVMe failed to connect to target %q on %q via NVMe: %w", targetQN, targetAddr, err)
		}
		logger.Debugf("NVMe connect output:\n%s", nvmeConnect)

		var waitSeconds2 = 15
		logger.Debugf("NVMe DEBUG. Pause! Let's do some extra tests. Timeout in seconds: %d", waitSeconds2)
		time.Sleep(time.Duration(waitSeconds2) * time.Second)

		nvmeList, err := shared.RunCommandContext(ctx, "nvme", "list")
		if err != nil {
			// return fmt.Errorf("NVMe failed to run nvm list: %s", err)
		}
		logger.Debugf("NVMe list output:\n%s", nvmeList)

		return nil
	}

	return connect(ctx, c, targetQN, targetAddresses, connectFunc)
}

// Disconnect terminates a connection with the target.
func (c *connectorNVMe) Disconnect(targetQN string) error {
	logger.Debugf("NVMe Disconnect()")

	// Find an existing NVMe session.
	session, err := c.findSession(targetQN)
	if err != nil {
		return err
	}

	// Disconnect from the NVMe target if there is an existing session.
	if session != nil {
		// Do not restrict the context as the operation is relatively short
		// and most importantly we do not want to "partially" disconnect from
		// the target, potentially leaving some unclosed sessions.
		_, err := shared.RunCommandContext(context.Background(), "nvme", "disconnect", "--nqn", targetQN)
		if err != nil {
			return fmt.Errorf("Failed disconnecting from NVMe target %q: %w", targetQN, err)
		}
	}

	return nil
}

// findSession returns an active NVMe subsystem (referred to as session for
// consistency across connectors) that matches the given targetQN.
// If the session is not found, nil is returned.
//
// This function handles the distinction between an "inactive" session (with no
// active controllers/connections) and a completely "non-existent" session. While
// checking "/sys/class/nvme" for active controllers is sufficient to identify if
// the session is currently in use, it does not account for cases where a session
// exists but is temporarily inactive (e.g., due to network issues). Removing
// such a session during this state would prevent it from automatically
// recovering once the connection is restored.
//
// To ensure we detect "existing" sessions, we first check for the session's
// presence in "/sys/class/nvme-subsystem", which tracks all associated NVMe
// subsystems regardless of their current connection state. If such session is
// found the function determines addresses of the active connections by checking
// "/sys/class/nvme", and returns a non-nil result (except if an error occurs).
func (c *connectorNVMe) findSession(targetQN string) (*session, error) {
	logger.Debugf("NVMe findSession()")

	// Base path for NVMe sessions/subsystems.
	subsysBasePath := "/sys/class/nvme-subsystem"

	// Retrieve the list of existing NVMe subsystems on this host.
	subsystems, err := os.ReadDir(subsysBasePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// If NVMe subsystems directory does not exist,
			// there is no sessions.
			return nil, nil
		}

		return nil, fmt.Errorf("Failed getting a list of existing NVMe subsystems: %w", err)
	}

	sessionID := ""
	for _, subsys := range subsystems {
		// Get the target NQN.
		nqnBytes, err := os.ReadFile(filepath.Join(subsysBasePath, subsys.Name(), "subsysnqn"))
		if err != nil {
			return nil, fmt.Errorf("Failed getting the target NQN for subystem %q: %w", subsys.Name(), err)
		}

		// Compare using contains, as targetQN may not be the entire NQN.
		// For example, PowerFlex targetQN is a substring of the full NQN.
		if strings.Contains(string(nqnBytes), targetQN) {
			// Found matching session.
			sessionID = strings.TrimPrefix(subsys.Name(), "nvme-subsys")
			break
		}
	}

	if sessionID == "" {
		// No matching session found.
		return nil, nil
	}

	session := &session{
		id:       sessionID,
		targetQN: targetQN,
	}

	basePath := "/sys/class/nvme"

	// Retrieve the list of currently active (operational) NVMe controllers.
	controllers, err := os.ReadDir(basePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// No active connections for any session.
			return session, nil
		}

		return nil, fmt.Errorf("Failed getting a list of existing NVMe subsystems: %w", err)
	}

	// Iterate over active NVMe devices and extract addresses from those
	// that correspond to the targetQN.
	for _, c := range controllers {
		// Get device's target NQN.
		nqnBytes, err := os.ReadFile(filepath.Join(basePath, c.Name(), "subsysnqn"))
		if err != nil {
			return nil, fmt.Errorf("Failed getting the target NQN for controller %q: %w", c.Name(), err)
		}

		// Compare using contains, as targetQN may not be the entire NQN.
		// For example, PowerFlex targetQN is a substring of the full NQN.
		if !strings.Contains(string(nqnBytes), targetQN) {
			// Subsystem does not belong to the targetQN.
			continue
		}

		// Read address file of an active NVMe connection.
		filePath := filepath.Join(basePath, c.Name(), "address")
		fileBytes, err := os.ReadFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("Failed getting connection address of controller %q for target %q: %w", c.Name(), targetQN, err)
		}

		// Extract the addresses from the file.
		// The "address" file contains one line per connection,
		// each in format "traddr=<ip>,trsvcid=<port>,...".
		for _, line := range strings.Split(string(fileBytes), "\n") {
			parts := strings.Split(strings.TrimSpace(line), ",")
			for _, part := range parts {
				addr, ok := strings.CutPrefix(part, "traddr=")
				if ok {
					session.addresses = append(session.addresses, addr)
					break
				}
			}
		}
	}

	return session, nil
}
