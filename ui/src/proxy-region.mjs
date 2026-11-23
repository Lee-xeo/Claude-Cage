const FLAG_RE = /[\u{1F1E6}-\u{1F1FF}]{2}/gu;

const KEYWORD_REGIONS = [
  ["HK", ["香港", "hong kong", "hongkong"]],
  ["TW", ["台湾", "台灣", "台北", "新北", "高雄", "彰化", "taiwan", "taipei"]],
  ["MO", ["澳门", "澳門", "macau", "macao"]],
  ["JP", ["日本", "东京", "東京", "大阪", "埼玉", "福冈", "福岡", "名古屋", "札幌", "japan", "tokyo", "osaka", "fukuoka", "nagoya", "saitama"]],
  ["SG", ["新加坡", "狮城", "獅城", "singapore"]],
  ["US", ["美国", "美國", "洛杉矶", "洛杉磯", "硅谷", "圣何塞", "聖何塞", "西雅图", "西雅圖", "纽约", "紐約", "芝加哥", "达拉斯", "達拉斯", "凤凰城", "鳳凰城", "旧金山", "舊金山", "迈阿密", "邁阿密", "华盛顿", "華盛頓", "弗吉尼亚", "俄勒冈", "亚特兰大", "united states", "america", "los angeles", "san jose", "silicon valley", "seattle", "new york", "chicago", "dallas", "phoenix", "san francisco", "miami", "ashburn", "atlanta", "oregon", "virginia", "california", "texas"]],
  ["KR", ["韩国", "韓國", "首尔", "首爾", "春川", "釜山", "korea", "seoul", "chuncheon", "busan"]],
  ["GB", ["英国", "英國", "伦敦", "倫敦", "曼彻斯特", "united kingdom", "britain", "england", "london", "manchester"]],
  ["DE", ["德国", "德國", "法兰克福", "法蘭克福", "柏林", "慕尼黑", "germany", "frankfurt", "berlin", "munich"]],
  ["FR", ["法国", "法國", "巴黎", "马赛", "france", "paris", "marseille"]],
  ["NL", ["荷兰", "荷蘭", "阿姆斯特丹", "netherlands", "holland", "amsterdam"]],
  ["CA", ["加拿大", "多伦多", "多倫多", "温哥华", "溫哥華", "蒙特利尔", "canada", "toronto", "vancouver", "montreal"]],
  ["AU", ["澳大利亚", "澳大利亞", "澳洲", "悉尼", "墨尔本", "australia", "sydney", "melbourne"]],
  ["IN", ["印度", "孟买", "孟買", "新德里", "海得拉巴", "india", "mumbai", "delhi", "bangalore", "hyderabad"]],
  ["TR", ["土耳其", "伊斯坦布尔", "伊斯坦堡", "turkey", "turkiye", "istanbul"]],
  ["MY", ["马来西亚", "馬來西亞", "吉隆坡", "malaysia", "kuala lumpur"]],
  ["TH", ["泰国", "泰國", "曼谷", "thailand", "bangkok"]],
  ["VN", ["越南", "胡志明", "河内", "河內", "vietnam", "ho chi minh", "hanoi"]],
  ["PH", ["菲律宾", "菲律賓", "马尼拉", "馬尼拉", "philippines", "manila"]],
  ["ID", ["印度尼西亚", "印尼", "雅加达", "雅加達", "indonesia", "jakarta"]],
  ["CH", ["瑞士", "苏黎世", "蘇黎世", "switzerland", "zurich"]],
  ["IE", ["爱尔兰", "愛爾蘭", "都柏林", "ireland", "dublin"]],
  ["IT", ["意大利", "義大利", "米兰", "米蘭", "罗马", "羅馬", "italy", "milan", "rome"]],
  ["ES", ["西班牙", "马德里", "馬德里", "巴塞罗那", "spain", "madrid", "barcelona"]],
  ["SE", ["瑞典", "斯德哥尔摩", "sweden", "stockholm"]],
  ["FI", ["芬兰", "芬蘭", "赫尔辛基", "finland", "helsinki"]],
  ["NO", ["挪威", "奥斯陆", "奧斯陸", "norway", "oslo"]],
  ["PL", ["波兰", "波蘭", "华沙", "華沙", "poland", "warsaw"]],
  ["RU", ["俄罗斯", "俄羅斯", "莫斯科", "russia", "moscow"]],
  ["BR", ["巴西", "圣保罗", "聖保羅", "brazil", "sao paulo"]],
  ["AR", ["阿根廷", "argentina", "buenos aires"]],
  ["AE", ["阿联酋", "阿聯酋", "迪拜", "杜拜", "dubai", "uae"]],
  ["ZA", ["南非", "约翰内斯堡", "south africa", "johannesburg"]],
  ["IL", ["以色列", "israel", "tel aviv"]],
  ["NZ", ["新西兰", "紐西蘭", "奥克兰", "new zealand", "auckland"]],
  ["MX", ["墨西哥", "mexico"]],
  ["CL", ["智利", "chile", "santiago"]],
  ["UA", ["乌克兰", "烏克蘭", "ukraine", "kyiv"]],
  ["AT", ["奥地利", "奧地利", "维也纳", "austria", "vienna"]],
  ["BE", ["比利时", "比利時", "布鲁塞尔", "belgium", "brussels"]],
  ["CZ", ["捷克", "布拉格", "czech", "prague"]],
  ["PT", ["葡萄牙", "里斯本", "portugal", "lisbon"]],
  ["LU", ["卢森堡", "盧森堡", "luxembourg"]],
  ["CN", ["中国", "中國", "大陆", "大陸", "北京", "上海", "深圳", "广州", "廣州", "杭州", "成都", "china", "beijing", "shanghai", "shenzhen"]],
];

// Three-letter IATA/ISO tokens (matched case-insensitively on letter boundaries)
const CODE3_TO_REGION = {
  HKG: "HK",
  TPE: "TW",
  KHH: "TW",
  MFM: "MO",
  JPN: "JP",
  NRT: "JP",
  HND: "JP",
  KIX: "JP",
  TYO: "JP",
  OSA: "JP",
  SIN: "SG",
  SGP: "SG",
  USA: "US",
  LAX: "US",
  SJC: "US",
  SFO: "US",
  SEA: "US",
  NYC: "US",
  JFK: "US",
  EWR: "US",
  ORD: "US",
  DFW: "US",
  PHX: "US",
  MIA: "US",
  IAD: "US",
  ATL: "US",
  KOR: "KR",
  ICN: "KR",
  SEL: "KR",
  GBR: "GB",
  LHR: "GB",
  LON: "GB",
  DEU: "DE",
  FRA: "DE",
  FRA1: "DE",
  CDG: "FR",
  PAR: "FR",
  NLD: "NL",
  AMS: "NL",
  CAN: "CA",
  YYZ: "CA",
  YVR: "CA",
  YUL: "CA",
  AUS: "AU",
  SYD: "AU",
  MEL: "AU",
  IND: "IN",
  BOM: "IN",
  DEL: "IN",
  TUR: "TR",
  IST: "TR",
  MYS: "MY",
  KUL: "MY",
  THA: "TH",
  BKK: "TH",
  VNM: "VN",
  SGN: "VN",
  HAN: "VN",
  PHL: "PH",
  MNL: "PH",
  IDN: "ID",
  CGK: "ID",
  CHE: "CH",
  ZRH: "CH",
  IRL: "IE",
  DUB: "IE",
  ITA: "IT",
  MXP: "IT",
  ESP: "ES",
  MAD: "ES",
  SWE: "SE",
  ARN: "SE",
  FIN: "FI",
  HEL: "FI",
  POL: "PL",
  WAW: "PL",
  RUS: "RU",
  MOW: "RU",
  BRA: "BR",
  GRU: "BR",
  ARE: "AE",
  DXB: "AE",
  NZL: "NZ",
  AKL: "NZ",
};

// Two-letter uppercase codes matched only on strict non-ASCII-letter boundaries
// (e.g. "HK01", "1x SG", "[US]", "JP-02") to avoid English words like "in"/"us"/"at".
const CODE2_TO_REGION = {
  HK: "HK",
  TW: "TW",
  MO: "MO",
  JP: "JP",
  SG: "SG",
  US: "US",
  KR: "KR",
  UK: "GB",
  GB: "GB",
  DE: "DE",
  FR: "FR",
  NL: "NL",
  CA: "CA",
  AU: "AU",
  IN: "IN",
  TR: "TR",
  MY: "MY",
  TH: "TH",
  VN: "VN",
  PH: "PH",
  ID: "ID",
  CH: "CH",
  IE: "IE",
  IT: "IT",
  ES: "ES",
  SE: "SE",
  FI: "FI",
  NO: "NO",
  PL: "PL",
  RU: "RU",
  BR: "BR",
  AR: "AR",
  AE: "AE",
  ZA: "ZA",
  IL: "IL",
  NZ: "NZ",
  MX: "MX",
  CL: "CL",
  UA: "UA",
  AT: "AT",
  BE: "BE",
  CZ: "CZ",
  PT: "PT",
  LU: "LU",
  CN: "CN",
};

function flagToCode(flag) {
  const points = [...flag].map((ch) => ch.codePointAt(0) - 0x1f1e6 + 65);
  if (points.length !== 2) return "";
  const code = String.fromCharCode(points[0], points[1]);
  return code === "UK" ? "GB" : code;
}

export function regionFlag(code) {
  if (!code || code === "ZZ" || code === "TW" || !/^[A-Z]{2}$/.test(code)) return "";
  return String.fromCodePoint(
    0x1f1e6 + code.charCodeAt(0) - 65,
    0x1f1e6 + code.charCodeAt(1) - 65,
  );
}

const displayNameCache = new Map();

export function regionLabel(code, locale) {
  const lang = locale || (typeof document !== "undefined" && document.documentElement.lang) || "zh-CN";
  let formatter = displayNameCache.get(lang);
  if (!formatter) {
    try {
      formatter = new Intl.DisplayNames([lang], { type: "region", style: "short" });
    } catch {
      formatter = new Intl.DisplayNames(["zh-CN"], { type: "region", style: "short" });
    }
    displayNameCache.set(lang, formatter);
  }
  try {
    return formatter.of(code || "ZZ") || code || "ZZ";
  } catch {
    return code || "ZZ";
  }
}

const KEYWORDS_BY_LENGTH = KEYWORD_REGIONS
  .flatMap(([code, keywords]) => keywords.map(kw => [kw, code]))
  .sort((a, b) => b[0].length - a[0].length);

export function detectNodeRegion(name) {
  const text = String(name || "");
  if (!text) return "ZZ";

  // 1. Flag emoji (highest confidence)
  const flagMatch = text.match(FLAG_RE);
  if (flagMatch) {
    const code = flagToCode(flagMatch[0]);
    if (code) return code;
  }

  // 2. Unambiguous Chinese / English country & city keywords
  // The longest keyword wins ("印度尼西亚" before "印度"), and a Latin
  // keyword must stand as a word ("rome" is not part of "chrome").
  const lower = text.toLowerCase();
  for (const [kw, code] of KEYWORDS_BY_LENGTH) {
    const at = lower.indexOf(kw);
    if (at < 0) continue;
    if (!/^[\x00-\x7f]+$/.test(kw)) return code;
    for (let i = at; i >= 0; i = lower.indexOf(kw, i + 1)) {
      const before = lower[i - 1] || "", after = lower[i + kw.length] || "";
      if (!/[a-z]/.test(before) && !/[a-z]/.test(after)) return code;
    }
  }

  // 3. Three-letter IATA / ISO codes bounded by non-letters
  const upper = text.toUpperCase();
  for (const match of upper.matchAll(/(?<![A-Z])([A-Z]{3})(?![A-Z])/g)) {
    const mapped = CODE3_TO_REGION[match[1]];
    if (mapped) return mapped;
  }

  // 4. Two-letter uppercase region codes bounded by non-letters (e.g. "HK01", "1x US", "JP-2")
  for (const match of text.matchAll(/(?<![A-Za-z])([A-Z]{2})(?![A-Za-z])/g)) {
    const mapped = CODE2_TO_REGION[match[1]];
    if (mapped) return mapped;
  }

  return "ZZ";
}

export function groupNodesByRegion(nodes, locale) {
  const lang = locale || (typeof document !== "undefined" && document.documentElement.lang) || "zh-CN";
  const buckets = new Map();
  for (const node of nodes) {
    const code = detectNodeRegion(node.name);
    let bucket = buckets.get(code);
    if (!bucket) {
      bucket = {
        code,
        flag: regionFlag(code),
        label: regionLabel(code, lang),
        nodes: [],
      };
      buckets.set(code, bucket);
    }
    bucket.nodes.push(node);
  }
  return [...buckets.values()].sort((a, b) => {
    if (a.code === "ZZ") return 1;
    if (b.code === "ZZ") return -1;
    if (b.nodes.length !== a.nodes.length) return b.nodes.length - a.nodes.length;
    return a.label.localeCompare(b.label, lang);
  });
}
