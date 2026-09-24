package id3

// v22Upgrade maps an ID3v2.2 three-character frame identifier to its v2.3/v2.4
// four-character equivalent, per the ID3v2.2 and v2.3 specifications. v2.2 is
// read in full and written back as v2.3, so the rest of the codec deals only
// with modern IDs.
var v22Upgrade = map[string]string{
	"BUF": "RBUF", "CNT": "PCNT", "COM": "COMM", "CRA": "AENC", "CRM": "ENCR",
	"ETC": "ETCO", "EQU": "EQUA", "GEO": "GEOB", "IPL": "IPLS", "LNK": "LINK",
	"MCI": "MCDI", "MLL": "MLLT", "PIC": "APIC", "POP": "POPM", "REV": "RVRB",
	"RVA": "RVAD", "SLT": "SYLT", "STC": "SYTC", "TAL": "TALB", "TBP": "TBPM",
	"TCM": "TCOM", "TCO": "TCON", "TCP": "TCMP", "TCR": "TCOP", "TDA": "TDAT",
	"TDY": "TDLY", "TEN": "TENC", "TFT": "TFLT", "TIM": "TIME", "TKE": "TKEY",
	"TLA": "TLAN", "TLE": "TLEN", "TMT": "TMED", "TOA": "TOPE", "TOF": "TOFN",
	"TOL": "TOLY", "TOR": "TORY", "TOT": "TOAL", "TP1": "TPE1", "TP2": "TPE2",
	"TP3": "TPE3", "TP4": "TPE4", "TPA": "TPOS", "TPB": "TPUB", "TRC": "TSRC",
	"TRD": "TRDA", "TRK": "TRCK", "TS2": "TSO2", "TSA": "TSOA", "TSC": "TSOC",
	"TSI": "TSIZ", "TSP": "TSOP", "TSS": "TSSE", "TST": "TSOT", "TT1": "TIT1",
	"TT2": "TIT2", "TT3": "TIT3", "TXT": "TEXT", "TXX": "TXXX", "TYE": "TYER",
	"UFI": "UFID", "ULT": "USLT", "WAF": "WOAF", "WAR": "WOAR", "WAS": "WOAS",
	"WCM": "WCOM", "WCP": "WCOP", "WPB": "WPUB", "WXX": "WXXX",
}

// upgradeV22ID returns the modern identifier for a v2.2 ID and whether it is
// known.
func upgradeV22ID(id string) (string, bool) {
	up, ok := v22Upgrade[id]
	return up, ok
}
