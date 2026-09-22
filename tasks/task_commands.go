package tasks

type taskCommand struct {
	name string
	args []string
	env  []string
}

func shellCommand(goos string, cmdPath string, environment []string) taskCommand {
	if goos == "windows" {
		return taskCommand{
			name: "powershell.exe",
			args: []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", cmdPath},
			env:  environment,
		}
	}

	return taskCommand{
		name: "sh",
		args: []string{"-c", cmdPath},
		env:  environment,
	}
}

func taskApplyManualCommand(goos string, releemDir string) taskCommand {
	if goos == "windows" {
		return powershellConfigurerCommand(releemDir, []string{"-Apply", "-NonInteractive"}, nil)
	}

	return shellCommand(goos, releemDir+"/mysqlconfigurer.sh -a", []string{"RELEEM_RESTART_SERVICE=1"})
}

func taskGenerateConfigCommand(goos string, releemDir string) taskCommand {
	if goos == "windows" {
		return taskCommand{name: releemDir + "\\releem-agent.exe", args: []string{"-f"}}
	}

	return shellCommand(goos, releemDir+"/releem-agent -f", nil)
}

func taskQueriesOptimizationCommand(goos string, releemDir string) taskCommand {
	if goos == "windows" {
		return taskCommand{name: releemDir + "\\releem-agent.exe", args: []string{"--task=queries_optimization"}}
	}

	return shellCommand(goos, releemDir+"/releem-agent --task=queries_optimization", nil)
}

func taskUpdateCommand(goos string, releemDir string) taskCommand {
	if goos == "windows" {
		return powershellConfigurerCommand(releemDir, []string{"-Update"}, nil)
	}

	return shellCommand(goos, releemDir+"/mysqlconfigurer.sh -u", nil)
}

func taskApplyAutomaticCommand(goos string, releemDir string, applyMode string, restart bool) taskCommand {
	if goos == "windows" {
		args := []string{"-Apply", "-NonInteractive", "-ApplyMode", applyMode}
		if !restart {
			args = append(args, "-NoRestart")
		}
		return powershellConfigurerCommand(releemDir, args, nil)
	}

	restartValue := "0"
	if restart {
		restartValue = "1"
	}

	return shellCommand(goos, releemDir+"/mysqlconfigurer.sh -s automatic "+applyMode, []string{"RELEEM_RESTART_SERVICE=" + restartValue})
}

func taskRollbackCommand(goos string, releemDir string) taskCommand {
	if goos == "windows" {
		return powershellConfigurerCommand(releemDir, []string{"-Rollback"}, []string{"RELEEM_ROLLBACK_CONFIRM=1"})
	}

	return shellCommand(goos, releemDir+"/mysqlconfigurer.sh -r", []string{"RELEEM_RESTART_SERVICE=1"})
}

func powershellConfigurerCommand(releemDir string, scriptArgs []string, environment []string) taskCommand {
	args := []string{
		"-NoProfile",
		"-NonInteractive",
		"-ExecutionPolicy",
		"Bypass",
		"-File",
		releemDir + "\\mysqlconfigurer.ps1",
	}
	args = append(args, scriptArgs...)

	return taskCommand{
		name: "powershell.exe",
		args: args,
		env:  environment,
	}
}

// func releemPath(goos string, releemDir string, fileName string) string {
// 	if goos == "windows" {
// 		return strings.TrimRight(releemDir, `\/`) + `\` + fileName
// 	}

// 	return strings.TrimRight(releemDir, "/") + "/" + fileName
// }
