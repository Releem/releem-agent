package models

import (
	"database/sql"
	"sync"

	"github.com/Releem/mysqlconfigurer/config"
)

type MetricType byte
type MetricIntervalType byte

type MetricValue struct {
	Name  string
	Value string
}
type MetricGroupValue map[string]interface{}

type ModeType struct {
	Name string
	Type string
}
type Metrics struct {
	System struct {
		Info    MetricGroupValue
		Conf    MetricGroupValue
		Metrics MetricGroupValue
	}
	DB struct {
		Metrics struct {
			Status                                MetricGroupValue
			TotalTables                           uint64
			TotalMyisamIndexes                    uint64
			Engine                                map[string]MetricGroupValue
			CountQueriesLatency                   uint64
			Databases                             []string
			InnoDBEngineStatus                    string
			CountEnabledEventsStatementsConsumers uint64
			ProcessList                           []MetricGroupValue
		}
		Conf struct {
			Variables MetricGroupValue
		}
		Info           MetricGroupValue
		Queries        []MetricGroupValue
		DatabaseSchema map[string][]MetricGroupValue
	}
	ReleemAgent struct {
		Info  MetricGroupValue
		Tasks Task
		Conf  config.Config
	}
}

type Metric map[string]MetricGroupValue

// type Metric interface {
// 	// GetProvider() string
// 	// GetType() MetricType
// 	// GetValue() string
// 	// GetName() string
// }

type Task struct {
	ID       int    `json:"task_id"`
	TypeID   int    `json:"task_type_id"`
	Details  string `json:"task_details"`
	Status   int    `json:"task_status"`
	Output   string `json:"task_output"`
	Error    string `json:"task_error"`
	ExitCode int    `json:"task_exit_code"`
}

type MetricContext interface {
	GetApiKey() string
	GetEnv() string
	GetMemoryLimit() int
	GetReleemConfDir() string
}

type MetricsGatherer interface {
	GetMetrics(metrics *Metrics) error
}

type MetricsRepeater interface {
	ProcessMetrics(context MetricContext, metrics Metrics, Mode ModeType) (string, error)
}

var (
	DB                      *sql.DB
	SampleQueries           map[string]string
	SampleQueriesMutex      sync.RWMutex
	CountEnabledConsumers   uint64
	PgStatStatementsEnabled bool
)
