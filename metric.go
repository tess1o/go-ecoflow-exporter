package main

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// generateMetricName takes a rawMetric string (for example "pd.wireUsedTime") received from Ecoflow Rest API.
// the function returns two values: metricName and deviceMetric name.
// metricName is used as a metric name in prometheus
// deviceMetricName is used as unique key from all metrics (device serial number + metric name). It's required to store all metrics in a map.
// The function converts rawMetric to prometheus compatible value (pd_wire_used_time)
// As a result it returns two values:
// metricName: prefix + prometheus compatible value (ecoflow_pd_wire_used_time)
// deviceMetricName: device serial number + metricName (R13124123123213_ecoflow_pd_wire_used_time)
// Example:
// rawMetric: pd.wireUsedTime
// prefix: ecoflow
// deviceSn: R13124123123213
//
// return values:
// metricName: ecoflow_pd_wire_used_time
// deviceMetricName: R13124123123213_ecoflow_pd_wire_used_time

func generateMetricName(rawMetric string, prefix string, deviceSn string) (string, string, error) {
	prometheusName, err := ecoflowParamToPrometheusMetric(rawMetric)
	if err != nil {
		return "", "", err
	}
	metricName := prefix + "_" + prometheusName
	deviceMetricName := deviceSn + "_" + metricName
	return metricName, deviceMetricName, nil
}

// ecoflowParamToPrometheusMetric takes a metricKey string and converts it to a Prometheus compatible name.
// It replaces the dot "." with an underscore "_" and converts any uppercase letters to lowercase,
// adding an underscore before each uppercase letter except when it's preceded by an underscore.
// The converted metricKey string must adhere to the Prometheus data model pattern [a-zA-Z_:][a-zA-Z0-9_:]*.
// If the conversion fails, an error is returned.
//
// Example:
//
//	metricKey: "pd.wireUsedTime"
//	converted: "pd_wire_used_time"
//
// Args:
//
//	metricKey: The original metric key string.
//
// Returns:
//
//	The converted metric key string, nil if successful.
//	Returns an error if the conversion fails.
func ecoflowParamToPrometheusMetric(metricKey string) (string, error) {
	key := strings.Replace(metricKey, ".", "_", -1)
	runes := []rune(key)
	var newKey bytes.Buffer

	newKey.WriteString(strings.ToLower(string(runes[0])))

	for _, character := range runes[1:] {
		if unicode.IsUpper(character) && !strings.HasSuffix(newKey.String(), "_") {
			newKey.WriteString("_")
		}
		newKey.WriteString(strings.ToLower(string(character)))
	}

	matches, err := regexp.MatchString("[a-zA-Z_:][a-zA-Z0-9_:]*", newKey.String())
	if err != nil {
		return "", err
	}

	if !matches {
		return "", fmt.Errorf("ecoflow parameter `%s` can't be converted to prometheus name", metricKey)
	}

	return newKey.String(), nil
}
