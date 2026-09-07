package repeater

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/Releem/mysqlconfigurer/config"
	"github.com/Releem/mysqlconfigurer/models"
	"github.com/Releem/mysqlconfigurer/utils"
	logging "github.com/google/logger"

	"time"
)

type ReleemConfigurationsRepeater struct {
	logger        logging.Logger
	configuration *config.Config
}

var newHTTPClient = func() *http.Client {
	return &http.Client{Timeout: 10 * time.Minute}
}

func (repeater ReleemConfigurationsRepeater) ProcessMetrics(context models.MetricContext, metrics models.Metrics, Mode models.ModeType) (string, error) {
	defer utils.HandlePanic(repeater.configuration, repeater.logger)
	repeater.logger.V(5).Info(Mode.Name, Mode.Type)
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	if err := encoder.Encode(metrics); err != nil {
		return "", errors.New("Failed to encode metrics: " + err.Error())
	}
	repeater.logger.V(5).Info("Result Send data: ", buffer.String())
	var api_domain, subdomain, domain string
	env := context.GetEnv()

	switch env {
	case "dev2":
		subdomain = "dev2."
	case "dev":
		subdomain = "dev."
	case "stage":
		subdomain = "stage."
	default:
		subdomain = ""
	}
	if repeater.configuration.ReleemRegion == "EU" || repeater.configuration.ReleemRegion == "eu" {
		domain = "eu.releem.com"
	} else {
		domain = "releem.com"
	}
	api_domain = "https://api.queries." + subdomain + domain + "/v2/"

	switch Mode.Name {
	case "Configurations":
		switch Mode.Type {
		case "Set":
			api_domain = api_domain + "db/config/set"
		case "Get", "GetJson":
			api_domain = api_domain + "db/config/get"
		case "GetInitial":
			api_domain = api_domain + "db/config/initial"
		default:
			api_domain = api_domain + "db/config/set"
		}
	case "Metrics":
		switch Mode.Type {
		case "Queries":
			api_domain = api_domain + "db/metrics/queries"
		default:
			api_domain = api_domain + "db/metrics"
		}
	case "Event":
		api_domain = api_domain + "events/" + Mode.Type
	case "Task":
		switch Mode.Type {
		case "Get":
			api_domain = api_domain + "tasks/get"
		case "Status":
			api_domain = api_domain + "tasks/status"
		}
	case "TaskByName":
		api_domain = api_domain + "tasks/by-name/" + Mode.Type
	}
	repeater.logger.V(5).Info(api_domain)

	req, err := http.NewRequest(http.MethodPost, api_domain, &buffer)
	if err != nil {
		return "", errors.New("Request: could not create request: " + err.Error())
	}
	req.Header.Set("x-releem-api-key", context.GetApiKey())
	if Mode.Name == "Configurations" && Mode.ApplyMode != "" {
		req.Header.Set("X-Releem-Apply-Mode", Mode.ApplyMode)
	}
	if Mode.Name == "Configurations" && Mode.Type == "GetJson" {
		req.Header.Set("Accept", "application/json")
	}
	res, err := newHTTPClient().Do(req)
	if err != nil {
		return "", errors.New("Request: error making http request: " + err.Error())
	}
	defer res.Body.Close()

	body_res, err := io.ReadAll(res.Body)
	if err != nil {
		return "", errors.New("Response: error read body request: " + err.Error())
	}
	if res.StatusCode != 200 && res.StatusCode != 201 {
		return "", errors.New("Response: status code: " + strconv.Itoa(res.StatusCode) + " Response: body:\n" + string(body_res))
	}
	repeater.logger.V(5).Info("Response: status code: ", res.StatusCode)
	repeater.logger.V(5).Info("Response: body:\n", string(body_res))

	if Mode.Name == "Configurations" && Mode.Type != "GetJson" {
		var config_filename string
		if Mode.Type == "GetInitial" {
			config_filename = "initial_config_mysql.cnf"
		} else {
			db_type := repeater.configuration.GetDatabaseType()
			switch db_type {
			case "mysql":
				config_filename = "z_aiops_mysql.cnf"
			case "postgresql":
				config_filename = "z_aiops_postgresql.conf"
			default:
				config_filename = "z_aiops_mysql.cnf"
			}
		}
		err = os.WriteFile(context.GetReleemConfDir()+"/"+config_filename, body_res, 0644)
		if err != nil {
			return "", errors.New("WriteFile: Error write to file: " + err.Error())
		}
	}
	return string(body_res), nil
}

func NewReleemConfigurationsRepeater(configuration *config.Config, logger logging.Logger) ReleemConfigurationsRepeater {
	return ReleemConfigurationsRepeater{logger, configuration}
}
