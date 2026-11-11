package tenants

// DefaultDepartmentName is the single department seeded into every new
// tenant by CreateTenant. Admins can rename, deprecate, or extend it.
const DefaultDepartmentName = "General"

// DefaultLevel is one ordered tier inside the default department.
// Rank grows with seniority: rank 1 is Employee, rank 4 is CEO. Rank
// does not imply access cascade (see spec).
type DefaultLevel struct {
	Name string
	Rank int32
}

// DefaultLevels are the four ordered levels of the General department.
// Order matters: the slice is consumed in array order when computing the
// bootstrap admin's seed membership (last entry = top of the ladder).
var DefaultLevels = []DefaultLevel{
	{Name: "Employee", Rank: 1},
	{Name: "Manager", Rank: 2},
	{Name: "Director", Rank: 3},
	{Name: "CEO", Rank: 4},
}

// BootstrapAdminLevelName names the level granted to the bootstrap admin
// during CreateTenant so they land with one membership at the top of
// the General ladder. Must match a Name in DefaultLevels.
const BootstrapAdminLevelName = "CEO"
