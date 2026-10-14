package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"net/url"
	"os"
	"os/signal"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/alecthomas/kingpin/v2"
	"github.com/shatteredsilicon/tool-agent/pkg/logger"
	"github.com/shatteredsilicon/tool-agent/pkg/report"
	"gopkg.in/ini.v1"
	"gopkg.in/yaml.v2"
)

var DefaultTuningFile string

var (
	logLevel  logger.AllowedLevel
	logFormat logger.AllowedFormat
)

var (
	cfg          = new(config)
	setByUserMap = make(map[string]bool)
)

func setByUserFlagAction() func(ctx *kingpin.ParseContext) error {
	executed := false

	return func(pc *kingpin.ParseContext) error {
		if executed {
			return nil
		}

		for _, elem := range pc.Elements {
			if elem.Clause == nil {
				continue
			}

			flagClause, ok := elem.Clause.(*kingpin.FlagClause)
			if !ok || flagClause == nil {
				continue
			}

			setByUserMap[flagClause.Model().Name] = true
		}

		executed = true
		return nil
	}
}

type toolList []report.ReportSource

func (tl *toolList) Set(value string) error {
	var source report.ReportSource
	if err := json.Unmarshal([]byte(value), &source); err != nil {
		return fmt.Errorf("invalid JSON %q: %w", value, err)
	}
	*tl = append(*tl, source)
	return nil
}

func (tl *toolList) String() string {
	return ""
}

func (tl *toolList) IsCumulative() bool {
	return true
}

var (
	configPath = kingpin.Flag(
		"config",
		"Path of config file",
	).Default("/opt/ss/tool-agent/tool-agent.conf").String()

	serverConfigFile = kingpin.Flag(
		"server-config",
		"Path of server config file",
	).String()

	interval = kingpin.Flag(
		"interval",
		"interval of tools being ran. supported suffixes are ns, µs, ms, s, m and h",
	).Duration()

	tools toolList
)

func init() {
	kingpin.Flag(
		"log-level",
		"Log level for logging",
	).HintOptions(logger.AllowedLevels...).SetValue(&logLevel)
	kingpin.Flag(
		"log-format",
		"Log format for logging",
	).HintOptions(logger.AllowedFormats...).SetValue(&logFormat)

	kingpin.Flag(
		"tool",
		"repeated JSON string representing a tool to run",
	).SetValue(&tools)

	kingpin.Flag(
		"server_address",
		"server address",
	).StringVar(&cfg.ServerAddress)
	kingpin.Flag(
		"client_name",
		"client name",
	).StringVar(&cfg.ClientName)
	kingpin.Flag(
		"server_user",
		"server user",
	).StringVar(&cfg.ServerUser)
	kingpin.Flag(
		"server_password",
		"server password",
	).StringVar(&cfg.ServerPassword)
	kingpin.Flag(
		"server_ssl",
		"whether to use ssl",
	).BoolVar(&cfg.ServerSSL)
	kingpin.Flag(
		"server_insecure_ssl",
		"whether to use insecure ssl",
	).BoolVar(&cfg.ServerInsecureSSL)
	kingpin.Flag(
		"managed_api_path",
		"ssm-managed api path",
	).StringVar(&cfg.ManagedAPIPath)

	kingpin.HelpFlag.Short('h')
	kingpin.CommandLine.PreAction(setByUserFlagAction())
	kingpin.Parse()
}

func main() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	reconnectSigChan := make(chan os.Signal, 1)
	signal.Notify(reconnectSigChan, syscall.SIGHUP) // kill -HUP PID

	intSigChan := make(chan os.Signal, 1)
	signal.Notify(intSigChan, syscall.SIGINT) // CTRL-C

	iniCfg, err := ini.Load(*configPath)
	if err != nil {
		log.Fatalf("failed to load config file %s: %s", *configPath, err.Error())
	}

	if err := iniCfg.MapTo(cfg); err != nil {
		log.Fatalf("failed to map config: %s", err.Error())
	}

	reportSources := make(map[string]report.ReportSource)
	for _, sectionName := range iniCfg.SectionStrings() {
		if sectionName == ini.DefaultSection || sectionName == "" {
			continue
		}

		var rs report.ReportSource
		if err := iniCfg.Section(sectionName).MapTo(&rs); err != nil {
			log.Fatalf("failed to map report source '%s': %s", sectionName, err.Error())
		}

		rs.Name = sectionName
		reportSources[rs.Name] = rs
	}
	for _, tool := range tools {
		reportSources[tool.Name] = tool
	}

	// override flag value with config value
	// if it's not set
	overrideFlags()

	if *serverConfigFile != "" {
		serverConfigBytes, err := os.ReadFile(*serverConfigFile)
		if err != nil {
			log.Fatalf("failed to read server config file %s: %s", *serverConfigFile, err.Error())
		}
		if err := yaml.Unmarshal(serverConfigBytes, &cfg.serverConfig); err != nil {
			log.Fatalf("failed to unmarshal server config file: %s", err.Error())
		}
	}

	var userInfo *url.Userinfo
	if cfg.ServerUser != "" {
		userInfo = url.UserPassword(cfg.ServerUser, cfg.ServerPassword)
	}
	scheme := "http"
	if cfg.ServerInsecureSSL || cfg.ServerSSL {
		scheme = "https"
	}

	serverURL := &url.URL{
		Scheme: scheme,
		User:   userInfo,
		Host:   cfg.ServerAddress,
		Path:   cfg.ManagedAPIPath,
	}

	logger := logger.NewLogger(&logLevel, &logFormat)
	report := report.NewReport(cfg.ClientName, logger, *interval, slices.Collect(maps.Values(reportSources)), serverURL)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer func() {

		}()

		report.Start(ctx)
	}()

LOOP:
	for {
		select {
		case <-sigChan:
			break LOOP
		case <-intSigChan:
			break LOOP
		case <-reconnectSigChan:
		}
	}

	cancel()
}

type config struct {
	LogLevel     string        `ini:"log-level"`
	LogFormat    string        `ini:"log-format"`
	ServerConfig string        `ini:"server-config"`
	Interval     time.Duration `ini:"interval"`

	serverConfig
}

type serverConfig struct {
	ServerAddress     string `yaml:"server_address"`
	ClientName        string `yaml:"client_name"`
	ServerUser        string `yaml:"server_user,omitempty"`
	ServerPassword    string `yaml:"server_password,omitempty"`
	ServerSSL         bool   `yaml:"server_ssl,omitempty"`
	ServerInsecureSSL bool   `yaml:"server_insecure_ssl,omitempty"`
	ManagedAPIPath    string `yaml:"managed_api_path"`
}

func configVisit(visitFn func(string, string, reflect.Value)) {
	type item struct {
		value   reflect.Value
		section string
	}

	items := []item{
		{
			value:   reflect.ValueOf(cfg).Elem(),
			section: "",
		},
	}
	for i := 0; i < len(items); i++ {
		for j := 0; j < items[i].value.Type().NumField(); j++ {
			fieldValue := items[i].value.Field(j)
			fieldType := items[i].value.Type().Field(j)
			section := items[i].section
			key := strings.SplitN(fieldType.Tag.Get("ini"), ",", 2)[0]
			if yamlKey, exists := fieldType.Tag.Lookup("yaml"); exists {
				key = strings.SplitN(yamlKey, ",", 2)[0]
			}

			if key == "-" {
				continue
			}

			if fieldValue.Kind() == reflect.Struct {
				if fieldValue.CanAddr() {
					if section == "" {
						section = key
					} else if section != key {
						section = fmt.Sprintf("%s.%s", section, key)
					}

					items = append(items, item{
						value:   fieldValue.Addr().Elem(),
						section: section,
					})
				}
				continue
			} else if fieldValue.Kind() == reflect.Ptr && fieldValue.IsNil() {
				continue
			}

			visitFn(section, key, fieldValue)
		}
	}
}

func overrideFlags() {
	configVisit(func(section, key string, fieldValue reflect.Value) {
		flagKey := fmt.Sprintf("%s.%s", section, key)
		if section == "" {
			flagKey = key
		}

		setByUser := setByUserMap[flagKey]
		kingpinF := kingpin.CommandLine.GetFlag(flagKey)
		if setByUser || kingpinF == nil {
			return
		}

		var values []reflect.Value
		if fieldValue.Kind() == reflect.Slice {
			for i := 0; i < fieldValue.Len(); i++ {
				values = append(values, fieldValue.Index(i))
			}
		} else {
			values = []reflect.Value{fieldValue}
		}

		for i := range values {
			switch values[i].Kind() {
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Float32, reflect.Int64:
				if fieldValue.Type() == reflect.TypeOf(time.Duration(0)) {
					kingpinF.Model().Value.Set(time.Duration(fieldValue.Int()).String())
				} else {
					kingpinF.Model().Value.Set(strconv.FormatInt(values[i].Int(), 10))
				}
			case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
				kingpinF.Model().Value.Set(strconv.FormatUint(values[i].Uint(), 10))
			case reflect.Bool:
				kingpinF.Model().Value.Set(strconv.FormatBool(values[i].Bool()))
			case reflect.Ptr:
				if !values[i].IsNil() {
					if values[i].Elem().Kind() == reflect.Bool {
						kingpinF.Model().Value.Set(strconv.FormatBool(values[i].Elem().Bool()))
					} else {
						kingpinF.Model().Value.Set(values[i].Elem().String())
					}
				}
			default:
				kingpinF.Model().Value.Set(values[i].String())
			}
		}
	})
}
