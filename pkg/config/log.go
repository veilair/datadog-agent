// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package config

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	seelogCfg "github.com/DataDog/datadog-agent/pkg/config/seelog"
	"github.com/DataDog/datadog-agent/pkg/util/log"
	"github.com/DataDog/datadog-agent/pkg/util/scrubber"
	"github.com/cihub/seelog"
)

// LoggerName specifies the name of an instantiated logger.
type LoggerName string

type contextFormat uint8

const (
	jsonFormat = contextFormat(iota)
	textFormat
	logDateFormat = "2006-01-02 15:04:05 MST" // see time.Format for format syntax
)

var syslogTLSConfig *tls.Config

var seelogConfig *seelogCfg.Config
var jmxSeelogConfig *seelogCfg.Config

func getLogDateFormat() string {
	if Datadog.GetBool("log_format_rfc3339") {
		return time.RFC3339
	}
	return logDateFormat
}

func createQuoteMsgFormatter(params string) seelog.FormatterFunc {
	return func(message string, level seelog.LogLevel, context seelog.LogContextInterface) interface{} {
		return strconv.Quote(message)
	}
}

func getSyslogTLSKeyPair() (*tls.Certificate, error) {
	var syslogTLSKeyPair *tls.Certificate
	if Datadog.IsSet("syslog_pem") && Datadog.IsSet("syslog_key") {
		cert := Datadog.GetString("syslog_pem")
		key := Datadog.GetString("syslog_key")

		if cert == "" && key == "" {
			return nil, nil
		} else if cert == "" || key == "" {
			return nil, fmt.Errorf("Both a PEM certificate and key must be specified to enable TLS")
		}

		keypair, err := tls.LoadX509KeyPair(cert, key)
		if err != nil {
			return nil, err
		}

		syslogTLSKeyPair = &keypair
	}

	return syslogTLSKeyPair, nil
}

// SetupLogger sets up a logger with the specified logger name and log level
// if a non empty logFile is provided, it will also log to the file
// a non empty syslogURI will enable syslog, and format them following RFC 5424 if specified
// you can also specify to log to the console and in JSON format
func SetupLogger(loggerName LoggerName, logLevel, logFile, syslogURI string, syslogRFC, logToConsole, jsonFormat bool) error {
	seelogLogLevel, err := validateLogLevel(logLevel)
	if err != nil {
		return err
	}
	seelogConfig, err = buildLoggerConfig(loggerName, seelogLogLevel, logFile, syslogURI, syslogRFC, logToConsole, jsonFormat)
	if err != nil {
		return err
	}
	loggerInterface, err := GenerateLoggerInterface(seelogConfig)
	if err != nil {
		return err
	}
	_ = seelog.ReplaceLogger(loggerInterface)
	log.SetupLogger(loggerInterface, seelogLogLevel)
	scrubber.AddStrippedKeys(Datadog.GetStringSlice("flare_stripped_keys"))
	return nil
}

// SetupJMXLogger sets up a logger with JMX logger name and log level
// if a non empty logFile is provided, it will also log to the file
// a non empty syslogURI will enable syslog, and format them following RFC 5424 if specified
// you can also specify to log to the console and in JSON format
func SetupJMXLogger(loggerName LoggerName, logLevel, logFile, syslogURI string, syslogRFC, logToConsole, jsonFormat bool) error {
	seelogLogLevel, err := validateLogLevel(logLevel)
	if err != nil {
		return err
	}
	jmxSeelogConfig, err = buildLoggerConfig(loggerName, seelogLogLevel, logFile, syslogURI, syslogRFC, logToConsole, jsonFormat)
	if err != nil {
		return err
	}
	jmxLoggerInterface, err := GenerateLoggerInterface(jmxSeelogConfig)
	if err != nil {
		return err
	}
	log.SetupJMXLogger(jmxLoggerInterface, seelogLogLevel)
	return nil
}

func buildLoggerConfig(loggerName LoggerName, seelogLogLevel, logFile, syslogURI string, syslogRFC, logToConsole, jsonFormat bool) (*seelogCfg.Config, error) {
	formatID := "common"
	if jsonFormat {
		formatID = "json"
	}

	config := seelogCfg.NewSeelogConfig(string(loggerName), seelogLogLevel, formatID, buildJSONFormat(loggerName), buildCommonFormat(loggerName), syslogRFC)
	config.EnableConsoleLog(logToConsole)
	config.EnableFileLogging(logFile, Datadog.GetSizeInBytes("log_file_max_size"), uint(Datadog.GetInt("log_file_max_rolls")))

	if syslogURI != "" { // non-blank uri enables syslog
		syslogTLSKeyPair, err := getSyslogTLSKeyPair()
		if err != nil {
			return nil, err
		}
		var useTLS bool
		if syslogTLSKeyPair != nil {
			useTLS = true
			syslogTLSConfig = &tls.Config{
				Certificates:       []tls.Certificate{*syslogTLSKeyPair},
				InsecureSkipVerify: Datadog.GetBool("syslog_tls_verify"),
			}
		}
		config.ConfigureSyslog(syslogURI, useTLS)
	}
	return config, nil
}

//GenerateLoggerInterface return a logger Interface from a log config
func GenerateLoggerInterface(logConfig *seelogCfg.Config) (seelog.LoggerInterface, error) {
	configTemplate, err := logConfig.Render()
	if err != nil {
		return nil, err
	}

	loggerInterface, err := seelog.LoggerFromConfigAsString(configTemplate)
	if err != nil {
		return nil, err
	}

	return loggerInterface, nil
}

// ErrorLogWriter is a Writer that logs all written messages with the global seelog logger
// at an error level
type ErrorLogWriter struct {
	AdditionalDepth int
}

func (s *ErrorLogWriter) Write(p []byte) (n int, err error) {
	log.ErrorStackDepth(s.AdditionalDepth, strings.TrimSpace(string(p)))
	return len(p), nil
}

var levelToSyslogSeverity = map[seelog.LogLevel]int{
	// Mapping to RFC 5424 where possible
	seelog.TraceLvl:    7,
	seelog.DebugLvl:    7,
	seelog.InfoLvl:     6,
	seelog.WarnLvl:     4,
	seelog.ErrorLvl:    3,
	seelog.CriticalLvl: 2,
	seelog.Off:         7,
}

func createSyslogHeaderFormatter(params string) seelog.FormatterFunc {
	facility := 20
	rfc := false

	ps := strings.Split(params, ",")
	if len(ps) == 2 {
		i, err := strconv.Atoi(ps[0])
		if err == nil && i >= 0 && i <= 23 {
			facility = i
		}

		rfc = (ps[1] == "true")
	} else {
		fmt.Println("badly formatted syslog header parameters - using defaults")
	}

	pid := os.Getpid()
	appName := filepath.Base(os.Args[0])

	if rfc { // RFC 5424
		return func(message string, level seelog.LogLevel, context seelog.LogContextInterface) interface{} {
			return fmt.Sprintf("<%d>1 %s %d - -", facility*8+levelToSyslogSeverity[level], appName, pid)
		}
	}

	// otherwise old-school logging
	return func(message string, level seelog.LogLevel, context seelog.LogContextInterface) interface{} {
		return fmt.Sprintf("<%d>%s[%d]:", facility*8+levelToSyslogSeverity[level], appName, pid)
	}
}

// SyslogReceiver implements seelog.CustomReceiver
type SyslogReceiver struct {
	enabled bool
	uri     *url.URL
	tls     bool
	conn    net.Conn
}

func getSyslogConnection(uri *url.URL, secure bool) (net.Conn, error) {
	var conn net.Conn
	var err error

	// local
	localNetNames := []string{"unixgram", "unix"}
	if uri == nil {
		addrs := []string{"/dev/log", "/var/run/syslog", "/var/run/log"}
		for _, netName := range localNetNames {
			for _, addr := range addrs {
				conn, err = net.Dial(netName, addr)
				if err == nil { // on success
					return conn, nil
				}
			}
		}
	} else {
		switch uri.Scheme {
		case "unix", "unixgram":
			fmt.Printf("Trying to connect to: %s", uri.Path)
			for _, netName := range localNetNames {
				conn, err = net.Dial(netName, uri.Path)
				if err == nil {
					break
				}
			}
		case "udp":
			conn, err = net.Dial(uri.Scheme, uri.Host)
		case "tcp":
			if secure {
				conn, err = tls.Dial("tcp", uri.Host, syslogTLSConfig)
			} else {
				conn, err = net.Dial("tcp", uri.Host)
			}
		}
		if err == nil {
			return conn, nil
		}
	}

	return nil, errors.New("Unable to connect to syslog")
}

// ReceiveMessage process current log message
func (s *SyslogReceiver) ReceiveMessage(message string, level seelog.LogLevel, context seelog.LogContextInterface) error {
	if !s.enabled {
		return nil
	}

	if s.conn != nil {
		_, err := s.conn.Write([]byte(message))
		if err == nil {
			return nil
		}
	}

	// try to reconnect - close the connection first just in case
	//                    we don't want fd leaks here.
	if s.conn != nil {
		s.conn.Close()
	}
	conn, err := getSyslogConnection(s.uri, s.tls)
	if err != nil {
		return err
	}

	s.conn = conn
	_, err = s.conn.Write([]byte(message))
	fmt.Printf("Retried: %v\n", message)
	return err
}

// AfterParse parses the receiver configuration
func (s *SyslogReceiver) AfterParse(initArgs seelog.CustomReceiverInitArgs) error {
	var conn net.Conn
	var ok bool
	var err error

	s.enabled = true
	uri, ok := initArgs.XmlCustomAttrs["uri"]
	if ok && uri != "" {
		url, err := url.ParseRequestURI(uri)
		if err != nil {
			s.enabled = false
		}

		s.uri = url
	}

	tls, ok := initArgs.XmlCustomAttrs["tls"]
	if ok {
		// if certificate specified it should already be in pool
		if tls == "true" {
			s.tls = true
		}
	}

	if !s.enabled {
		return errors.New("bad syslog receiver configuration - disabling")
	}

	conn, err = getSyslogConnection(s.uri, s.tls)
	if err != nil {
		fmt.Printf("%v\n", err)
		return nil
	}
	s.conn = conn

	return nil
}

// Flush is a NOP in current implementation
func (s *SyslogReceiver) Flush() {
	// Nothing to do here...
}

// Close is a NOP in current implementation
func (s *SyslogReceiver) Close() error {
	return nil
}

func parseShortFilePath(params string) seelog.FormatterFunc {
	return func(message string, level seelog.LogLevel, context seelog.LogContextInterface) interface{} {
		return extractShortPathFromFullPath(context.FullPath())
	}
}

func extractShortPathFromFullPath(fullPath string) string {
	shortPath := ""
	if strings.Contains(fullPath, "-agent/") {
		// We want to trim the part containing the path of the project
		// ie DataDog/datadog-agent/ or DataDog/datadog-process-agent/
		slices := strings.Split(fullPath, "-agent/")
		shortPath = slices[len(slices)-1]
	} else {
		// For logging from dependencies, we want to log e.g.
		// "collector@v0.35.0/service/collector.go"
		slices := strings.Split(fullPath, "/")
		atSignIndex := len(slices) - 1
		for ; atSignIndex > 0; atSignIndex-- {
			if strings.Contains(slices[atSignIndex], "@") {
				break
			}
		}
		shortPath = strings.Join(slices[atSignIndex:], "/")
	}
	return shortPath
}

func createExtraJSONContext(params string) seelog.FormatterFunc {
	return func(message string, level seelog.LogLevel, context seelog.LogContextInterface) interface{} {
		contextList, ok := context.CustomContext().([]interface{})
		if len(contextList) == 0 || !ok {
			return ""
		}
		return extractContextString(jsonFormat, contextList)
	}
}

func createExtraTextContext(params string) seelog.FormatterFunc {
	return func(message string, level seelog.LogLevel, context seelog.LogContextInterface) interface{} {
		contextList, ok := context.CustomContext().([]interface{})
		if len(contextList) == 0 || !ok {
			return ""
		}
		return extractContextString(textFormat, contextList)
	}
}

func extractContextString(format contextFormat, contextList []interface{}) string {
	if len(contextList) == 0 || len(contextList)%2 != 0 {
		return ""
	}

	builder := strings.Builder{}
	if format == jsonFormat {
		builder.WriteString(",")
	}

	for i := 0; i < len(contextList); i += 2 {
		key, val := contextList[i], contextList[i+1]
		// Only add if key is string
		if keyStr, ok := key.(string); ok {
			addToBuilder(&builder, keyStr, val, format, i == len(contextList)-2)
		}
	}

	if format != jsonFormat {
		builder.WriteString(" | ")
	}

	return builder.String()
}

func addToBuilder(builder *strings.Builder, key string, value interface{}, format contextFormat, isLast bool) {
	var buf []byte
	appendFmt(builder, format, key, buf)
	builder.WriteString(":")
	switch val := value.(type) {
	case string:
		appendFmt(builder, format, val, buf)
	default:
		appendFmt(builder, format, fmt.Sprintf("%v", val), buf)
	}
	if !isLast {
		builder.WriteString(",")
	}
}

func appendFmt(builder *strings.Builder, format contextFormat, s string, buf []byte) {
	if format == jsonFormat {
		buf = buf[:0]
		buf = strconv.AppendQuote(buf, s)
		builder.Write(buf)
	} else {
		builder.WriteString(s)
	}
}

// ChangeLogLevel immediately changes the log level to the given one.
func ChangeLogLevel(level string) error {
	seelogLogLevel, err := validateLogLevel(level)
	if err != nil {
		return err
	}
	// We create a new logger to propagate the new log level everywhere seelog is used (including dependencies)
	seelogConfig.SetLogLevel(seelogLogLevel)
	configTemplate, err := seelogConfig.Render()
	if err != nil {
		return err
	}

	logger, err := seelog.LoggerFromConfigAsString(configTemplate)
	if err != nil {
		return err
	}
	seelog.ReplaceLogger(logger) //nolint:errcheck

	// We wire the new logger with the Datadog logic
	return log.ChangeLogLevel(logger, seelogLogLevel)
}

func validateLogLevel(logLevel string) (string, error) {
	seelogLogLevel := strings.ToLower(logLevel)
	if seelogLogLevel == "warning" { // Common gotcha when used to agent5
		seelogLogLevel = "warn"
	}

	if _, found := seelog.LogLevelFromString(seelogLogLevel); !found {
		return "", fmt.Errorf("unknown log level: %s", seelogLogLevel)
	}
	return seelogLogLevel, nil
}

func init() {
	seelog.RegisterCustomFormatter("CustomSyslogHeader", createSyslogHeaderFormatter) //nolint:errcheck
	seelog.RegisterCustomFormatter("ShortFilePath", parseShortFilePath)               //nolint:errcheck
	seelog.RegisterCustomFormatter("ExtraJSONContext", createExtraJSONContext)        //nolint:errcheck
	seelog.RegisterCustomFormatter("ExtraTextContext", createExtraTextContext)        //nolint:errcheck
	seelog.RegisterReceiver("syslog", &SyslogReceiver{})                              //nolint:errcheck
}
