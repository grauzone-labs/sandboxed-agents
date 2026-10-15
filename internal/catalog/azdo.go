package catalog

import (
	"fmt"
	"net/url"
	"strings"
)

// AzureDevOpsOrganization validates a credential-free HTTPS organization URL and derives the Git username used by Azure clone URLs.
func AzureDevOpsOrganization(raw string) (organization, username string, err error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(raw, "\r\n\x00") || strings.ContainsAny(u.Path, "\r\n\x00") {
		return "", "", fmt.Errorf("--organization requires an HTTPS organization URL without credentials, query, or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	username = strings.Split(u.Hostname(), ".")[0]
	if strings.EqualFold(u.Hostname(), "dev.azure.com") {
		username = strings.TrimPrefix(u.Path, "/")
		if username == "" || strings.Contains(username, "/") {
			return "", "", fmt.Errorf("--organization requires https://dev.azure.com/ORGANIZATION")
		}
	}
	return u.String(), username, nil
}
