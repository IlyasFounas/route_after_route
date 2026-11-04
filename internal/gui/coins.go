package gui

import "strings"

// coinNames gives a readable name to the most common tickers. Others are
// shown by ticker only.
var coinNames = map[string]string{
	"BTC": "Bitcoin", "ETH": "Ethereum", "SOL": "Solana", "BNB": "BNB", "XRP": "XRP",
	"DOGE": "Dogecoin", "ADA": "Cardano", "TRX": "TRON", "AVAX": "Avalanche", "LINK": "Chainlink",
	"DOT": "Polkadot", "LTC": "Litecoin", "SHIB": "Shiba Inu", "PEPE": "Pepe", "SUI": "Sui",
	"TON": "Toncoin", "NEAR": "NEAR Protocol", "APT": "Aptos", "UNI": "Uniswap", "ATOM": "Cosmos",
	"XLM": "Stellar", "BCH": "Bitcoin Cash", "ETC": "Ethereum Classic", "FIL": "Filecoin",
	"ARB": "Arbitrum", "OP": "Optimism", "INJ": "Injective", "HBAR": "Hedera", "ICP": "Internet Computer",
	"AAVE": "Aave", "JUP": "Jupiter", "WIF": "dogwifhat", "BONK": "Bonk", "PYTH": "Pyth Network",
	"RENDER": "Render", "TAO": "Bittensor", "SEI": "Sei", "TIA": "Celestia", "ENA": "Ethena",
	"WLD": "Worldcoin", "JTO": "Jito", "RAY": "Raydium", "ALGO": "Algorand", "VET": "VeChain",
	"FLOKI": "Floki", "MKR": "Maker", "LDO": "Lido DAO", "STX": "Stacks", "IMX": "Immutable",
	"GRT": "The Graph", "SAND": "The Sandbox", "MANA": "Decentraland", "AXS": "Axie Infinity",
	"CRV": "Curve", "PENDLE": "Pendle", "ONDO": "Ondo", "TRUMP": "Official Trump", "PENGU": "Pudgy Penguins",
}

func coinName(base string) string { return coinNames[base] }

// baseOf returns the ticker of a pair: SOLUSDT → SOL.
func baseOf(pair string) string {
	if b, ok := strings.CutSuffix(pair, "USDT"); ok && b != "" {
		return b
	}
	return pair
}

// validLogoTicker reports whether base can have a logo (a plain ticker, not
// a shortened token mint).
func validLogoTicker(base string) bool {
	if base == "" || len(base) > 16 {
		return false
	}
	for _, c := range base {
		if (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// filterCoins keeps the coins whose ticker starts with q or whose name
// contains q (case-insensitive). A leading $ is a ticker: $SOL. The order is
// kept.
func filterCoins(coins []Coin, q string) []Coin {
	q = strings.ToLower(strings.TrimSpace(q))
	if t, ok := strings.CutPrefix(q, "$"); ok {
		var out []Coin
		for _, c := range coins {
			if strings.HasPrefix(strings.ToLower(c.Base), t) {
				out = append(out, c)
			}
		}
		return out
	}
	if q == "" {
		return coins
	}
	var out []Coin
	for _, c := range coins {
		if strings.HasPrefix(strings.ToLower(c.Base), q) || strings.Contains(strings.ToLower(coinName(c.Base)), q) {
			out = append(out, c)
		}
	}
	return out
}
