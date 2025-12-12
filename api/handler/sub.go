package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/bestnite/sub2sing-box/common"
	"github.com/bestnite/sub2sing-box/constant"
	"github.com/bestnite/sub2sing-box/util"
	"github.com/gin-gonic/gin"
)

var (
	winRegex      = regexp.MustCompile(`(?i)windows|win\d|win`)
	macRegex      = regexp.MustCompile(`(?i)macintosh|mac os x|macos|darwin|mac`)
	linuxRegex    = regexp.MustCompile(`(?i)linux`)
	browserRegex  = regexp.MustCompile(`(?i)chrome|firefox|safari|edge|opera|webkit|mozilla/[0-9]`)
	singBoxVerReg = regexp.MustCompile(`sing-box[/ ]v?(\d+)\.(\d+)`)
)

func extractSingBoxVersion(ua string) (major, minor int, ok bool) {
	matches := singBoxVerReg.FindStringSubmatch(ua)
	if len(matches) < 3 {
		return 0, 0, false
	}
	major, err1 := strconv.Atoi(matches[1])
	minor, err2 := strconv.Atoi(matches[2])
	return major, minor, err1 == nil && err2 == nil
}

func detectClientType(ua string) (client string, maj, min int) {
	lowerUA := strings.ToLower(ua)

	if browserRegex.MatchString(lowerUA) {
		return constant.ClientBrowser, 0, 0
	}

	if strings.Contains(lowerUA, "sfi/") {
		maj, min, _ = extractSingBoxVersion(ua)
		return constant.ClientIOS, maj, min
	}
	if strings.Contains(lowerUA, "sft/") {
		maj, min, _ = extractSingBoxVersion(ua)
		return constant.ClientTVOS, maj, min
	}
	if strings.Contains(lowerUA, "sfm/") {
		maj, min, _ = extractSingBoxVersion(ua)
		return constant.ClientMacOS, maj, min
	}
	if strings.Contains(lowerUA, "sfa/") {
		maj, min, _ = extractSingBoxVersion(ua)
		return constant.ClientAndroid, maj, min
	}

	if macRegex.MatchString(lowerUA) {
		return constant.MacOS, 0, 0
	}
	if winRegex.MatchString(lowerUA) {
		return constant.Windows, 0, 0
	}
	if linuxRegex.MatchString(lowerUA) {
		return constant.Linux, 0, 0
	}
	return constant.ClientUnknown, 0, 0
}

func fetchAndReturnRaw(c *gin.Context, subUrl string) {
	resp, err := http.Get(subUrl)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to fetch subscription: %s", err.Error())})
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to read subscription content: %s", err.Error())})
		return
	}
	c.Data(http.StatusOK, "text/plain; charset=utf-8", body)
}

func convertWithTemplate(c *gin.Context, subUrl, templatePath string) {

	data, err := util.LoadConfigIfNeeded()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load the sub2sing-box.json configuration file.\n" + err.Error()})
		return
	}

	if templatePath == constant.AppleClientConfigV1v11 {
		data.Version = constant.ClientVersionV1v11
	}

	data.Subscriptions = append(data.Subscriptions, subUrl)
	data.Template = templatePath
	groupRules := make(map[string][]string)
	if data.GroupRules != "" {
		err = json.Unmarshal([]byte(data.GroupRules), &groupRules)
		if err != nil {
			c.JSON(400, gin.H{
				"error": err.Error(),
			})
			return
		}
	}

	if data.Version == constant.ClientVersionV1v11 {
		j, err := json.Marshal(data)
		if err != nil {
			c.JSON(400, gin.H{
				"error": err.Error(),
			})
			return
		}
		cmd := exec.Command(
			"sub2sing-box-v1.11/sub2sing-box-v1.11",
			"convert",
			"-c",
			string(j),
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error":   err.Error(),
				"details": string(out),
			})
			return
		}
		c.String(http.StatusOK, string(out))
	} else {
		result, err := common.Convert(
			data.Subscriptions,
			data.Proxies,
			data.Template,
			data.Delete,
			data.Rename,
			data.Group,
			data.GroupType,
			data.SortKey,
			data.SortType,
			groupRules,
		)
		if err != nil {
			c.JSON(400, gin.H{
				"error": err.Error(),
			})
			return
		}
		c.String(200, result)
	}
}

func DirectSub(c *gin.Context) {

	subUrl := strings.TrimPrefix(c.Param("url"), "/")
	if c.Request.URL.RawQuery != "" {
		subUrl += "?" + c.Request.URL.RawQuery
	}

	subUrl, _ = url.QueryUnescape(subUrl)

	userAgent := c.Request.UserAgent()
	client, maj, min := detectClientType(userAgent)

	switch client {
	case constant.ClientBrowser, constant.ClientUnknown:
		fetchAndReturnRaw(c, subUrl)
	case constant.ClientIOS, constant.ClientTVOS:
		if maj == 1 && min == 11 {
			convertWithTemplate(c, subUrl, constant.AppleClientConfigV1v11)
		} else {
			convertWithTemplate(c, subUrl, constant.AppleClientConfig)
		}
	case constant.ClientMacOS:
		convertWithTemplate(c, subUrl, constant.AppleClientConfig)
	case constant.ClientAndroid:
		convertWithTemplate(c, subUrl, constant.AndroidClientConfig)
	case constant.Windows:
		convertWithTemplate(c, subUrl, constant.WinConfig)
	case constant.Linux:
		convertWithTemplate(c, subUrl, constant.LinuxConfig)
	case constant.MacOS:
		convertWithTemplate(c, subUrl, constant.AppleConfig)

	default:
		fetchAndReturnRaw(c, subUrl)
	}
}
