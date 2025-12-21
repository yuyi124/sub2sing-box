package common

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/bestnite/sub2sing-box/constant"
	"github.com/bestnite/sub2sing-box/model"
	"github.com/bestnite/sub2sing-box/parser"
	"github.com/bestnite/sub2sing-box/util"
	box "github.com/sagernet/sing-box"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	J "github.com/sagernet/sing/common/json"
)

var globalCtx = box.Context(context.Background(), include.InboundRegistry(), include.OutboundRegistry(), include.EndpointRegistry(), include.DNSTransportRegistry(), include.ServiceRegistry())

func Convert(
	subscriptions []string,
	proxies []string,
	templatePath string,
	delete string,
	rename map[string]string,
	enableGroup bool,
	groupType string,
	sortKey string,
	sortType string,
	groupRules map[string][]string,
) (string, error) {
	result := ""
	var err error

	if groupType == "" {
		groupType = C.TypeSelector
	}
	// 输入:订阅链接数组
	// 输出:代理服务器列表（[]model.Outbound）
	outbounds, err := ConvertSubscriptionsToSProxy(subscriptions)
	if err != nil {
		return "", err
	}

	// 处理单个或多个节点分享链接
	for _, proxy := range proxies {
		p, err := ConvertCProxyToSProxy(proxy)
		if err != nil {
			return "", err
		}
		outbounds = append(outbounds, p)
	}

	if delete != "" {
		outbounds, err = DeleteProxy(outbounds, delete)
		if err != nil {
			return "", err
		}
	}

	for k, v := range rename {
		outbounds, err = RenameProxy(outbounds, k, v)
		if err != nil {
			return "", err
		}
	}

	// 去重
	set := make(map[string]bool)
	deduplicatedOutbounds := make([]model.Outbound, 0)
	for _, p := range outbounds {

		jsonBytes, err := json.Marshal(p)
		if err != nil {
			return "", err
		}

		// key:"{tag:hk,type:vless,...}",value:false
		if _, exists := set[string(jsonBytes)]; !exists {
			set[string(jsonBytes)] = true
			deduplicatedOutbounds = append(deduplicatedOutbounds, p)
		}
	}
	outbounds = deduplicatedOutbounds

	tagSet := make(map[string]bool)
	// 去重之后，如果tag相同，节点配置不相同的部分，就使用"tag+数字加1"
	// key:"hk",value:false
	for i, p := range outbounds {
		if _, exists := tagSet[p.Tag]; exists {
			count := 1
			for {
				newTag := fmt.Sprintf("%s %d", p.Tag, count)
				if _, exists := tagSet[newTag]; !exists {
					outbounds[i].Tag = newTag
					break
				} else {
					count++
				}
			}
		}
	}

	if templatePath != "" {
		templateData, err := ReadTemplate(templatePath)
		if err != nil {
			return "", err
		}
		// 检查模板中是否包含国家代码占位符（如 <US>），或者包含国家分组相关的常量
		reg := regexp.MustCompile("\"<[A-Za-z]{2}>\"")
		group := false

		//"美国(US)"，如果模板数据中包含任何国家名称，则将变量 group 设置为 true。
		for _, v := range model.CountryEnglishName {
			if strings.Contains(templateData, v) {
				group = true
			}
		}

		hasAllCountryTags := strings.Contains(templateData, constant.AllCountryTags)
		hasCountryPlaceholder := reg.MatchString(templateData)
		//显式启用分组功能&&模板数据中包含所有国家分组的常量占位符
		// 未显式启用分组功能&&模板数据中包含类似 <US> 这样的国家代码占位符||模板数据中包含所有国家分组的常量占位符||模板数据中包含国家名称（在前面的代码中检测到）
		shouldGroup := enableGroup ||
			(!enableGroup && (hasCountryPlaceholder || hasAllCountryTags || group))

		if shouldGroup {
			// 获得所有原始节点和分组配置
			outbounds = AddCountryGroup(outbounds, groupType, sortKey, sortType, groupRules)
		}
		var template model.Options
		// 将templateData解析为model.Options结构体
		if template, err = J.UnmarshalExtendedContext[model.Options](globalCtx, []byte(templateData)); err != nil {
			return "", err
		}
		for _, v := range template.Options.Outbounds {
			template.Outbounds = append(template.Outbounds, (model.Outbound)(v))
		}
		for _, v := range template.Options.Inbounds {
			template.Inbounds = append(template.Inbounds, (model.Inbound)(v))
		}
		for _, v := range template.Options.Endpoints {
			template.Endpoints = append(template.Endpoints, (model.Endpoint)(v))
		}
		result, err = MergeTemplate(outbounds, &template, groupRules)
		if err != nil {
			return "", err
		}
	} else {
		outboundJsons := make([]string, 0)
		for _, p := range outbounds {
			b, err := json.Marshal(p)
			if err != nil {
				return "", err
			}
			outboundJsons = append(outboundJsons, string(b))
		}
		result = fmt.Sprintf("[%s]", strings.Join(outboundJsons, ","))
	}

	return string(result), nil
}

// US[{tag:"aws01",type:"vless"},{tag:"aws02",type:"vless"}]
func AddCountryGroup(proxies []model.Outbound, groupType string, sortKey string, sortType string, groupRules map[string][]string) []model.Outbound {
	// key: 国家代码,value: 节点
	newGroup := make(map[string]model.Outbound)
	// [] key为分组名称，value为该分组对应的正则表达式规则列表
	//{"US": ["US-CA", "US-TX"], "CN": ["CN-BJ", "CN-SH"]}
	groupRulesRegexps := make(map[string][]*regexp.Regexp)
	for k, v := range groupRules {
		for _, rule := range v {
			groupRulesRegexps[k] = append(groupRulesRegexps[k], regexp.MustCompile(rule))
		}
	}
	for _, p := range proxies {
		// 跳过 selector 和 url-test 类型的outbound
		if p.Type != C.TypeSelector && p.Type != C.TypeURLTest {
			//首先根据节点标签自动识别国家名称
			country := model.GetContryName(p.Tag)
			//遍历预编译的正则表达式规则
			//{"US": ["US-CA", "US-TX"], "CN": ["CN-BJ", "CN-SH"]}
			for k, rules := range groupRulesRegexps {
				for _, rule := range rules {
					//如果节点标签匹配某个规则，则将其分配到对应的自定义组
					if rule.MatchString(p.Tag) {
						// country = "US"
						country = k
						break
					}
				}
			}

			//key: 国家代码,value: 节点
			// 查找指定国家的分组是否已存在
			if group, ok := newGroup[country]; ok {
				//如果存在就添加节点
				AppendOutbound(&group, p.Tag)
				newGroup[country] = group
			} else {
				//如果不存在就创建新的分组
				if groupType == C.TypeSelector {
					newGroup[country] = model.Outbound{
						Tag:  country,
						Type: groupType,
						Options: option.SelectorOutboundOptions{
							Outbounds:                 []string{p.Tag},
							InterruptExistConnections: true,
						},
					}
				} else if groupType == C.TypeURLTest {
					newGroup[country] = model.Outbound{
						Tag:  country,
						Type: groupType,
						Options: option.URLTestOutboundOptions{
							Outbounds:                 []string{p.Tag},
							InterruptExistConnections: true,
						},
					}
				}
			}
		}
	}
	// 给groups数组排序
	var groups []model.Outbound
	for _, p := range newGroup {
		groups = append(groups, p)
	}
	if sortType != "" {
		if sortType == "asc" {
			switch sortKey {
			case "tag":
				sort.Sort(model.SortByTag(groups))
			case "num":
				sort.Sort(model.SortByNumber(groups))
			default:
				sort.Sort(model.SortByTag(groups))
			}
		} else {
			switch sortKey {
			case "tag":
				sort.Sort(sort.Reverse(model.SortByTag(groups)))
			case "num":
				sort.Sort(sort.Reverse(model.SortByNumber(groups)))
			default:
				sort.Sort(sort.Reverse(model.SortByTag(groups)))
			}
		}
	}
	//将原始代理节点和分组配置合并后返回
	return append(proxies, groups...)
}

func ReadTemplate(template string) (string, error) {
	var data string
	var err error
	isNetworkFile, _ := regexp.MatchString(`^https?://`, template)
	if isNetworkFile {
		data, err = util.Fetch(template, 3)
		if err != nil {
			return "", err
		}
		return data, nil
	} else {
		if !strings.Contains(template, string(filepath.Separator)) {
			path := filepath.Join("templates", template)
			if _, err := os.Stat(path); err == nil {
				template = path
			}
		}
		dataBytes, err := os.ReadFile(template)
		if err != nil {
			return "", err
		}
		return string(dataBytes), nil
	}
}

func MergeTemplate(outbounds []model.Outbound, template *model.Options, groupRules map[string][]string) (string, error) {
	var err error
	proxyTags := make([]string, 0)            // 存放节点Tag
	groupTags := make([]string, 0)            // 存放策略组的Tag
	groups := make(map[string]model.Outbound) // 国家代码 -> 对应的 Outbound
	rulesKeys := make([]string, 0)            // groupRules 的所有 key（即分组名）

	//将 groupRules 中的所有分组名称（如 "US", "CN" 等）收集到 rulesKeys 列表中，便于后续判断。
	for k := range groupRules {
		rulesKeys = append(rulesKeys, k)
	}

	//分类 outbounds：普通代理 vs 国家分组代理
	//遍历所有 outbounds：
	//
	//如果 p.Tag 是 groupRules 中的一个 key，或者被判定为“国家组”（通过 model.IsCountryGroup），则：
	//加入 groupTags
	//用正则 [A-Za-z]{2} 提取两个字母的国家代码（如 "US"）
	//将该 Outbound 存入 groups[country]
	//否则视为普通代理，加入 proxyTags

	for _, p := range outbounds {
		if slices.Contains(rulesKeys, p.Tag) || model.IsCountryGroup(p.Tag) {
			groupTags = append(groupTags, p.Tag)
			reg := regexp.MustCompile("[A-Za-z]{2}")
			country := reg.FindString(p.Tag)
			groups[country] = p
		} else {
			proxyTags = append(proxyTags, p.Tag)
		}
	}

	// 匹配"<>" 占位符
	reg := regexp.MustCompile("<[A-Za-z]{2}>")

	//只处理类型为 selector 或 url-test 的出站（因为它们需要指定可用的代理列表）。
	//对每个这样的出站，遍历其当前配置中的代理列表（通过 GetOutbounds 获取）。
	//对每个项 o：
	//如果是 AllProxyTags（常量，如 "all-proxies"）→ 替换为所有普通代理的 Tag
	//如果是 AllCountryTags（如 "all-countries"）→ 替换为所有国家分组代理的 Tag
	//如果匹配 <XX> 格式（如 <US>）→ 提取国家码，查找 groups 中对应的 Outbound，并将其内部代理列表展开（递归？）
	//否则保留原样（可能是具体代理名）

	for i, o := range template.Outbounds {
		outbound := (model.Outbound)(o)
		if outbound.Type == C.TypeSelector || outbound.Type == C.TypeURLTest {
			var parsedOutbound []string = make([]string, 0)
			for _, o := range GetOutbounds(&outbound) {
				if o == constant.AllProxyTags {
					parsedOutbound = append(parsedOutbound, proxyTags...)
				} else if o == constant.AllCountryTags {
					parsedOutbound = append(parsedOutbound, groupTags...)
				} else if reg.MatchString(o) {
					country := strings.ToUpper(strings.Trim(reg.FindString(o), "<>"))
					if group, ok := groups[country]; ok {
						parsedOutbound = append(parsedOutbound, GetOutbounds(&group)...)
					}
				} else {
					parsedOutbound = append(parsedOutbound, o)
				}
			}
			SetOutbounds(&template.Outbounds[i], parsedOutbound)
		}
	}

	//把原始的所有 outbounds 直接追加到模板的 Outbounds 列表末尾。
	//这样最终配置既包含模板中定义的（已解析）出站，也包含原始代理本身（避免丢失）。

	template.Outbounds = append(template.Outbounds, outbounds...)

	//如果 DNS 规则没有指定类型，默认设为 RuleTypeDefault，确保配置合法。
	for i := range template.DNS.Rules {
		if template.DNS.Rules[i].Type == "" {
			template.DNS.Rules[i].Type = C.RuleTypeDefault
		}
	}

	data, err := json.Marshal(template)
	if err != nil {
		return "", err
	}

	copied, err := copyDNSServerOptions(template.DNS.Servers)
	if err != nil {
		return "", err
	}

	dnsServersCopy := struct {
		Servers []model.DNSServerOptionsCopy `json:"servers"`
	}{
		Servers: copied,
	}

	dnsServersJson, err := json.MarshalIndent(dnsServersCopy, "", "  ")
	if err != nil {
		return "", err
	}

	result, err := ReplaceDNSServers(string(data), string(dnsServersJson))
	if err != nil {
		return "", err
	}
	return result, nil
}

func ReplaceDNSServers(originalJSON, newServersJSON string) (string, error) {
	var config map[string]interface{}
	if err := json.Unmarshal([]byte(originalJSON), &config); err != nil {
		return "", fmt.Errorf("failed to unmarshal original JSON: %w", err)
	}

	var newServers map[string]interface{}
	if err := json.Unmarshal([]byte(newServersJSON), &newServers); err != nil {
		return "", fmt.Errorf("failed to unmarshal new servers JSON: %w", err)
	}

	dns, ok := config["dns"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("field 'dns' missing or not an object in original JSON")
	}

	servers, ok := newServers["servers"].([]interface{})
	if !ok {
		return "", fmt.Errorf("field 'servers' missing or not an array in new servers JSON")
	}

	dns["servers"] = servers

	resultBytes, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal result JSON: %w", err)
	}

	return string(resultBytes), nil
}

func ConvertCProxyToSProxy(proxy string) (model.Outbound, error) {
	for prefix, parseFunc := range parser.ParserMap {
		if strings.HasPrefix(proxy, prefix) {
			proxy, err := parseFunc(proxy)
			if err != nil {
				return model.Outbound{}, err
			}
			return proxy, nil
		}
	}
	return model.Outbound{}, errors.New("unknown proxy format")
}

func ConvertSubscriptionsToSProxy(urls []string) ([]model.Outbound, error) {
	proxyList := make([]model.Outbound, 0)
	for _, url := range urls {
		data, err := util.Fetch(url, 3)
		if err != nil {
			return nil, err
		}
		proxy := data
		if !strings.Contains(data, "://") {
			proxy, err = util.DecodeBase64(data)
		}
		if err != nil {
			return nil, err
		}
		proxies := strings.Split(proxy, "\n")
		for _, p := range proxies {
			for prefix, parseFunc := range parser.ParserMap {
				if strings.HasPrefix(p, prefix) {
					proxy, err := parseFunc(p)
					if err != nil {
						return nil, err
					}
					proxyList = append(proxyList, proxy)
				}
			}
		}
	}
	return proxyList, nil
}

func DeleteProxy(proxies []model.Outbound, regex string) ([]model.Outbound, error) {
	reg, err := regexp.Compile(regex)
	if err != nil {
		return nil, err
	}
	var newProxies []model.Outbound
	for _, p := range proxies {
		if !reg.MatchString(p.Tag) {
			newProxies = append(newProxies, p)
		}
	}
	return newProxies, nil
}

func RenameProxy(proxies []model.Outbound, regex string, replaceText string) ([]model.Outbound, error) {
	reg, err := regexp.Compile(regex)
	if err != nil {
		return nil, err
	}
	for i, p := range proxies {
		if reg.MatchString(p.Tag) {
			proxies[i].Tag = reg.ReplaceAllString(p.Tag, replaceText)
		}
	}
	return proxies, nil
}

func SetOutbounds(outbound *model.Outbound, outbounds []string) {
	switch v := outbound.Options.(type) {
	case option.SelectorOutboundOptions:
		v.Outbounds = outbounds
		outbound.Options = v
	case option.URLTestOutboundOptions:
		v.Outbounds = outbounds
		outbound.Options = v
	case *option.SelectorOutboundOptions:
		v.Outbounds = outbounds
		outbound.Options = v
	case *option.URLTestOutboundOptions:
		v.Outbounds = outbounds
		outbound.Options = v
	}
}

func AppendOutbound(outbound *model.Outbound, outboundTag string) {
	switch v := outbound.Options.(type) {
	case option.SelectorOutboundOptions:
		v.Outbounds = append(v.Outbounds, outboundTag)
		outbound.Options = v
	case option.URLTestOutboundOptions:
		v.Outbounds = append(v.Outbounds, outboundTag)
		outbound.Options = v
	case *option.SelectorOutboundOptions:
		v.Outbounds = append(v.Outbounds, outboundTag)
		outbound.Options = v
	case *option.URLTestOutboundOptions:
		v.Outbounds = append(v.Outbounds, outboundTag)
		outbound.Options = v
	}
}

func GetOutbounds(outbound *model.Outbound) []string {
	switch v := outbound.Options.(type) {
	case option.SelectorOutboundOptions:
		return v.Outbounds
	case option.URLTestOutboundOptions:
		return v.Outbounds
	case *option.SelectorOutboundOptions:
		return v.Outbounds
	case *option.URLTestOutboundOptions:
		return v.Outbounds
	}
	return nil
}

func copyDNSServerOptions(src []option.DNSServerOptions) ([]model.DNSServerOptionsCopy, error) {
	var result []model.DNSServerOptionsCopy
	for _, s := range src {
		dst := model.DNSServerOptionsCopy{Type: s.Type, Tag: s.Tag}

		switch s.Type {
		case C.DNSTypeHTTPS, C.DNSTypeHTTP3:
			if opts, ok := s.Options.(*option.RemoteHTTPSDNSServerOptions); ok {
				dst.Server = opts.Server
				dst.Detour = opts.Detour
			}
		case C.DNSTypeQUIC:
			if opts, ok := s.Options.(*option.RemoteTLSDNSServerOptions); ok {
				dst.Server = opts.Server
				dst.Detour = opts.Detour
			}
		case C.DNSTypeFakeIP:
			if opts, ok := s.Options.(*option.FakeIPDNSServerOptions); ok {
				if opts.Inet4Range != nil {
					p4 := opts.Inet4Range.Build(netip.Prefix{}) // 如果无效，返回零值
					if p4.IsValid() {
						dst.Inet4Range = p4.String()
					}
				}
				if opts.Inet6Range != nil {
					p6 := opts.Inet6Range.Build(netip.Prefix{})
					if p6.IsValid() {
						dst.Inet6Range = p6.String()
					}
				}
			}
		case C.DNSTypeUDP, C.DNSTypeTCP, C.DNSTypeTLS:
			if opts, ok := s.Options.(*option.RemoteDNSServerOptions); ok {
				dst.Server = opts.Server
				dst.Detour = opts.Detour
			} else if opts, ok := s.Options.(*option.RemoteTLSDNSServerOptions); ok {
				dst.Server = opts.Server
				dst.Detour = opts.Detour
			}
		}

		extra, err := extractExtraFields(s.Options)
		if err == nil {
			if v, ok := extra["domain_resolver"].(string); ok {
				dst.DomainResolver = v
			}
		}

		result = append(result, dst)
	}
	return result, nil
}

func extractExtraFields(v any) (map[string]interface{}, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	return m, json.Unmarshal(data, &m)
}
