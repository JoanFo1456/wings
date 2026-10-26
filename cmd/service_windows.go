//go:build windows

package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	serviceName        = "Wings"
	serviceDisplayName = "Pelican Wings"
	serviceDescription = "Pelican Wings daemon, the control plane for the game servers of a Pelican Panel."
)

func init() {
	rootCommand.AddCommand(newServiceCommand())
}

// runAsWindowsService runs Wings under the Windows service control manager when
// that is what started the process. It returns false when Wings was started
// normally, from a terminal, and the command line should be handled as usual.
func runAsWindowsService(run func() error) bool {
	isService, err := svc.IsWindowsService()
	if err != nil || !isService {
		return false
	}
	// A failure to talk to the service manager is reported by the exit code.
	if err := svc.Run(serviceName, &wingsService{run: run}); err != nil {
		os.Exit(1)
	}
	return true
}

// wingsService adapts Wings to the Windows service control manager.
type wingsService struct {
	run func() error
}

// Execute starts Wings and then waits for the service manager to stop it. Wings
// has no graceful shutdown, just like when systemd stops it on Linux, so stopping
// the service simply ends the process once the stop has been acknowledged.
func (s *wingsService) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown

	status <- svc.Status{State: svc.StartPending}
	done := make(chan error, 1)
	go func() { done <- s.run() }()
	status <- svc.Status{State: svc.Running, Accepts: accepted}

	for {
		select {
		case err := <-done:
			// Wings returned by itself, so the service has to stop as well. A failure
			// is reported as such so that the recovery actions restart the service.
			if err != nil {
				return true, 1
			}
			return false, 0
		case req := <-requests:
			switch req.Cmd {
			case svc.Interrogate:
				status <- req.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				return false, 0
			}
		}
	}
}

func newServiceCommand() *cobra.Command {
	command := &cobra.Command{
		Use:          "service",
		Short:        "Install and manage Wings as a Windows service",
		SilenceUsage: true,
	}

	command.AddCommand(&cobra.Command{
		Use:   "install",
		Short: "Install Wings as a Windows service that starts automatically with the computer",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withHint(installService(cmd.Flags().Changed("config")))
		},
	})
	command.AddCommand(&cobra.Command{
		Use:   "uninstall",
		Short: "Stop and remove the Wings Windows service",
		Args:  cobra.NoArgs,
		RunE:  func(*cobra.Command, []string) error { return withHint(uninstallService()) },
	})
	command.AddCommand(&cobra.Command{
		Use:   "start",
		Short: "Start the Wings Windows service",
		Args:  cobra.NoArgs,
		RunE:  func(*cobra.Command, []string) error { return withHint(startService()) },
	})
	command.AddCommand(&cobra.Command{
		Use:   "stop",
		Short: "Stop the Wings Windows service",
		Args:  cobra.NoArgs,
		RunE:  func(*cobra.Command, []string) error { return withHint(stopService()) },
	})

	return command
}

// serviceArgs returns the arguments the service passes to Wings, which is only
// the location of the configuration file when it is not the default one.
func serviceArgs(configChanged bool, configPath string) []string {
	if !configChanged {
		return nil
	}
	return []string{"--config", configPath}
}

func installService(configChanged bool) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to locate the Wings executable: %w", err)
	}

	// The service does not start in the folder the command was run from.
	cfgPath, err := filepath.Abs(configPath)
	if err != nil {
		return err
	}

	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()

	if s, err := m.OpenService(serviceName); err == nil {
		s.Close()
		return fmt.Errorf("the %s service is already installed, remove it first with: wings service uninstall", serviceName)
	}

	s, err := m.CreateService(serviceName, exe, mgr.Config{
		DisplayName:      serviceDisplayName,
		Description:      serviceDescription,
		StartType:        mgr.StartAutomatic,
		DelayedAutoStart: true,
	}, serviceArgs(configChanged, cfgPath)...)
	if err != nil {
		return err
	}
	defer s.Close()

	// Restart Wings when it stops unexpectedly, for example when Docker is not
	// running yet. This is what systemd's Restart=on-failure does on Linux.
	actions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}
	if err := s.SetRecoveryActions(actions, 86400); err != nil {
		return fmt.Errorf("the service was installed, but its restart settings could not be applied: %w", err)
	}
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return fmt.Errorf("the service was installed, but its restart settings could not be applied: %w", err)
	}

	fmt.Printf("The %s service was installed and starts automatically with the computer.\n", serviceName)
	fmt.Println("Start it now with: wings service start")
	return nil
}

func uninstallService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("the %s service is not installed", serviceName)
	}
	defer s.Close()

	if err := stopAndWait(s); err != nil {
		return err
	}
	if err := s.Delete(); err != nil {
		return err
	}
	fmt.Printf("The %s service was removed.\n", serviceName)
	return nil
}

func startService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("the %s service is not installed, install it first with: wings service install", serviceName)
	}
	defer s.Close()

	if err := s.Start(); err != nil {
		return err
	}
	fmt.Printf("The %s service was started.\n", serviceName)
	return nil
}

func stopService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("the %s service is not installed", serviceName)
	}
	defer s.Close()

	if err := stopAndWait(s); err != nil {
		return err
	}
	fmt.Printf("The %s service was stopped.\n", serviceName)
	return nil
}

// stopAndWait stops the service if it is running and waits until it has stopped.
func stopAndWait(s *mgr.Service) error {
	status, err := s.Query()
	if err != nil {
		return err
	}
	if status.State == svc.Stopped {
		return nil
	}
	if status, err = s.Control(svc.Stop); err != nil {
		return err
	}
	deadline := time.Now().Add(30 * time.Second)
	for status.State != svc.Stopped {
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for the service to stop")
		}
		time.Sleep(300 * time.Millisecond)
		if status, err = s.Query(); err != nil {
			return err
		}
	}
	return nil
}

// withHint explains how to fix the most common reason a service command fails,
// not running in an elevated PowerShell window.
func withHint(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("%w (run this from a PowerShell window opened as Administrator)", err)
	}
	return err
}
