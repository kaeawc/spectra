package services

import "time"

type LaunchJob struct {
	Label            string    `json:"label"`
	Domain           string    `json:"domain,omitempty"`
	PID              int32     `json:"pid,omitempty"`
	LastExitStatus   int32     `json:"last_exit_status,omitempty"`
	OnDemand         bool      `json:"on_demand,omitempty"`
	Disabled         bool      `json:"disabled,omitempty"`
	KeepAlive        bool      `json:"keep_alive,omitempty"`
	RunAtLoad        bool      `json:"run_at_load,omitempty"`
	Program          string    `json:"program,omitempty"`
	ProgramArguments []string  `json:"program_arguments,omitempty"`
	PlistPath        string    `json:"plist_path,omitempty"`
	PlistMTime       time.Time `json:"plist_mtime,omitempty"`
	StartInterval    int       `json:"start_interval,omitempty"`
	StartCalendar    string    `json:"start_calendar,omitempty"`
	WatchPaths       []string  `json:"watch_paths,omitempty"`
}

type LaunchInventory struct {
	Jobs        []LaunchJob `json:"jobs"`
	CollectedAt time.Time   `json:"collected_at"`
}
