package tasks

import (
	"reflect"
	"testing"
)

func TestWindowsTaskCommands(t *testing.T) {
	releemDir := `C:\Program Files\ReleemAgent`

	tests := []struct {
		name string
		cmd  taskCommand
		want taskCommand
	}{
		{
			name: "type 0 applies config non-interactively",
			cmd:  taskApplyManualCommand("windows", releemDir),
			want: taskCommand{
				name: "powershell.exe",
				args: []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", `C:\Program Files\ReleemAgent\mysqlconfigurer.ps1`, "-Apply", "-NonInteractive"},
			},
		},
		{
			name: "type 1 generates config",
			cmd:  taskGenerateConfigCommand("windows", releemDir),
			want: taskCommand{
				name: `C:\Program Files\ReleemAgent\releem-agent.exe`,
				args: []string{"-f"},
			},
		},
		{
			name: "type 3 queues query optimization",
			cmd:  taskQueriesOptimizationCommand("windows", releemDir),
			want: taskCommand{
				name: `C:\Program Files\ReleemAgent\releem-agent.exe`,
				args: []string{"--task=queries_optimization"},
			},
		},
		{
			name: "type 4 applies config without restart",
			cmd:  taskApplyAutomaticCommand("windows", releemDir, "dynamic", false),
			want: taskCommand{
				name: "powershell.exe",
				args: []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", `C:\Program Files\ReleemAgent\mysqlconfigurer.ps1`, "-Apply", "-NonInteractive", "-ApplyMode", "dynamic", "-NoRestart"},
			},
		},
		{
			name: "type 5 applies config with restart",
			cmd:  taskApplyAutomaticCommand("windows", releemDir, "full", true),
			want: taskCommand{
				name: "powershell.exe",
				args: []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", `C:\Program Files\ReleemAgent\mysqlconfigurer.ps1`, "-Apply", "-NonInteractive", "-ApplyMode", "full"},
			},
		},
		{
			name: "rollback confirms non-interactively",
			cmd:  taskRollbackCommand("windows", releemDir),
			want: taskCommand{
				name: "powershell.exe",
				args: []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", `C:\Program Files\ReleemAgent\mysqlconfigurer.ps1`, "-Rollback"},
				env:  []string{"RELEEM_ROLLBACK_CONFIRM=1"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !reflect.DeepEqual(tt.cmd, tt.want) {
				t.Fatalf("command mismatch\nwant: %#v\n got: %#v", tt.want, tt.cmd)
			}
		})
	}
}

func TestLinuxTaskCommandsKeepExistingBehavior(t *testing.T) {
	releemDir := "/opt/releem"

	tests := []struct {
		name string
		cmd  taskCommand
		want taskCommand
	}{
		{
			name: "type 0 uses mysqlconfigurer.sh -a",
			cmd:  taskApplyManualCommand("linux", releemDir),
			want: taskCommand{
				name: "sh",
				args: []string{"-c", "/opt/releem/mysqlconfigurer.sh -a"},
				env:  []string{"RELEEM_RESTART_SERVICE=1"},
			},
		},
		{
			name: "type 1 uses releem-agent -f",
			cmd:  taskGenerateConfigCommand("linux", releemDir),
			want: taskCommand{
				name: "sh",
				args: []string{"-c", "/opt/releem/releem-agent -f"},
			},
		},
		{
			name: "type 3 uses releem-agent query optimization task",
			cmd:  taskQueriesOptimizationCommand("linux", releemDir),
			want: taskCommand{
				name: "sh",
				args: []string{"-c", "/opt/releem/releem-agent --task=queries_optimization"},
			},
		},
		{
			name: "type 4 uses automatic apply without restart",
			cmd:  taskApplyAutomaticCommand("linux", releemDir, "dynamic", false),
			want: taskCommand{
				name: "sh",
				args: []string{"-c", "/opt/releem/mysqlconfigurer.sh -s automatic dynamic"},
				env:  []string{"RELEEM_RESTART_SERVICE=0"},
			},
		},
		{
			name: "type 5 uses automatic apply with restart",
			cmd:  taskApplyAutomaticCommand("linux", releemDir, "full", true),
			want: taskCommand{
				name: "sh",
				args: []string{"-c", "/opt/releem/mysqlconfigurer.sh -s automatic full"},
				env:  []string{"RELEEM_RESTART_SERVICE=1"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !reflect.DeepEqual(tt.cmd, tt.want) {
				t.Fatalf("command mismatch\nwant: %#v\n got: %#v", tt.want, tt.cmd)
			}
		})
	}
}
