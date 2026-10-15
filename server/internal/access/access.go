package access

// Decision 对应验收里的判定顺序：401，然后 403，然后 404。
type Decision int

const (
	Allow Decision = iota
	Unauthenticated
	Forbidden
	EmailUnverified
	NotFound
)

type Principal struct {
	Role     string
	Verified bool
}

const (
	ActList       = "list"
	ActOpen       = "open"
	ActOpenGraph  = "open_graph"
	ActAdmin      = "admin"
	ActUpload     = "upload"
	ActAsk        = "ask"
)

// Gate 不查业务表。打开资源时，管理员直接 NotFound，避免借角色读到别人的资料。
func Gate(p *Principal, action string, restrictEmail bool) Decision {
	if p == nil {
		return Unauthenticated
	}
	switch action {
	case ActAdmin:
		if p.Role != "admin" {
			return Forbidden
		}
		return Allow
	case ActOpenGraph:
		if p.Role == "admin" {
			return NotFound
		}
		return Allow
	case ActOpen:
		if p.Role == "admin" {
			return NotFound
		}
		return Allow
	case ActList:
		return Allow
	case ActUpload, ActAsk:
		if restrictEmail && p.Role != "admin" && !p.Verified {
			return EmailUnverified
		}
		if p.Role == "admin" {
			return NotFound
		}
		return Allow
	default:
		return Forbidden
	}
}
