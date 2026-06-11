// Package cnmap maps ESPN English team display names to Chinese names and
// flag emojis. Unknown names fall back to the original string.
package cnmap

import (
	"fmt"
	"regexp"
)

type team struct {
	cn   string
	flag string
}

// All 48 teams qualified for the 2026 World Cup, keyed by ESPN displayName.
var teams = map[string]team{
	"Algeria":            {"阿尔及利亚", "🇩🇿"},
	"Argentina":          {"阿根廷", "🇦🇷"},
	"Australia":          {"澳大利亚", "🇦🇺"},
	"Austria":            {"奥地利", "🇦🇹"},
	"Belgium":            {"比利时", "🇧🇪"},
	"Bosnia-Herzegovina": {"波黑", "🇧🇦"},
	"Brazil":             {"巴西", "🇧🇷"},
	"Canada":             {"加拿大", "🇨🇦"},
	"Cape Verde":         {"佛得角", "🇨🇻"},
	"Colombia":           {"哥伦比亚", "🇨🇴"},
	"Congo DR":           {"民主刚果", "🇨🇩"},
	"Croatia":            {"克罗地亚", "🇭🇷"},
	"Curaçao":            {"库拉索", "🇨🇼"},
	"Czechia":            {"捷克", "🇨🇿"},
	"Ecuador":            {"厄瓜多尔", "🇪🇨"},
	"Egypt":              {"埃及", "🇪🇬"},
	"England":            {"英格兰", "🏴󠁧󠁢󠁥󠁮󠁧󠁿"},
	"France":             {"法国", "🇫🇷"},
	"Germany":            {"德国", "🇩🇪"},
	"Ghana":              {"加纳", "🇬🇭"},
	"Haiti":              {"海地", "🇭🇹"},
	"Iran":               {"伊朗", "🇮🇷"},
	"Iraq":               {"伊拉克", "🇮🇶"},
	"Ivory Coast":        {"科特迪瓦", "🇨🇮"},
	"Japan":              {"日本", "🇯🇵"},
	"Jordan":             {"约旦", "🇯🇴"},
	"Mexico":             {"墨西哥", "🇲🇽"},
	"Morocco":            {"摩洛哥", "🇲🇦"},
	"Netherlands":        {"荷兰", "🇳🇱"},
	"New Zealand":        {"新西兰", "🇳🇿"},
	"Norway":             {"挪威", "🇳🇴"},
	"Panama":             {"巴拿马", "🇵🇦"},
	"Paraguay":           {"巴拉圭", "🇵🇾"},
	"Portugal":           {"葡萄牙", "🇵🇹"},
	"Qatar":              {"卡塔尔", "🇶🇦"},
	"Saudi Arabia":       {"沙特阿拉伯", "🇸🇦"},
	"Scotland":           {"苏格兰", "🏴󠁧󠁢󠁳󠁣󠁴󠁿"},
	"Senegal":            {"塞内加尔", "🇸🇳"},
	"South Africa":       {"南非", "🇿🇦"},
	"South Korea":        {"韩国", "🇰🇷"},
	"Spain":              {"西班牙", "🇪🇸"},
	"Sweden":             {"瑞典", "🇸🇪"},
	"Switzerland":        {"瑞士", "🇨🇭"},
	"Tunisia":            {"突尼斯", "🇹🇳"},
	"Türkiye":            {"土耳其", "🇹🇷"},
	"United States":      {"美国", "🇺🇸"},
	"Uruguay":            {"乌拉圭", "🇺🇾"},
	"Uzbekistan":         {"乌兹别克斯坦", "🇺🇿"},
}

// Knockout-bracket placeholder names ESPN uses before teams are decided.
var placeholders = []struct {
	re *regexp.Regexp
	cn string // format string receiving submatches
}{
	{regexp.MustCompile(`^Group ([A-L]) Winner$`), "%s组第一"},
	{regexp.MustCompile(`^Group ([A-L]) 2nd Place$`), "%s组第二"},
	{regexp.MustCompile(`^Third Place Group (.+)$`), "小组第三（%s组之一）"},
	{regexp.MustCompile(`^Round of 32 (\d+) Winner$`), "32强赛第%s场胜者"},
	{regexp.MustCompile(`^Round of 16 (\d+) Winner$`), "16强赛第%s场胜者"},
	{regexp.MustCompile(`^Quarterfinal (\d+) Winner$`), "1/4决赛第%s场胜者"},
	{regexp.MustCompile(`^Semifinal (\d+) Winner$`), "半决赛第%s场胜者"},
	{regexp.MustCompile(`^Semifinal (\d+) Loser$`), "半决赛第%s场负者"},
}

// EnglishName resolves a Chinese team name back to the ESPN displayName;
// returns the input unchanged when unknown (it may already be English).
func EnglishName(cn string) string {
	for en, t := range teams {
		if t.cn == cn {
			return en
		}
	}
	return cn
}

// Name returns the Chinese name for an ESPN displayName, or the original
// string when unknown.
func Name(displayName string) string {
	if t, ok := teams[displayName]; ok {
		return t.cn
	}
	for _, p := range placeholders {
		if m := p.re.FindStringSubmatch(displayName); m != nil {
			return fmt.Sprintf(p.cn, m[1])
		}
	}
	return displayName
}

// Flag returns the flag emoji, or empty string when unknown.
func Flag(displayName string) string {
	if t, ok := teams[displayName]; ok {
		return t.flag
	}
	return ""
}

// Full returns "🇲🇽墨西哥" style flag+name; for unknown teams just the name.
func Full(displayName string) string {
	if t, ok := teams[displayName]; ok {
		return t.flag + t.cn
	}
	return Name(displayName)
}
