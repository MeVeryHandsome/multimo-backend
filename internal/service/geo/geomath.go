package geo

import (
	"fmt"
	"math"
)

const earthRadius = 6371008.8

// haversine is the great-circle distance in metres.
func haversine(lat1, lon1, lat2, lon2 float64) float64 {
	p1, p2 := lat1*math.Pi/180, lat2*math.Pi/180
	dp, dl := p2-p1, (lon2-lon1)*math.Pi/180
	a := math.Sin(dp/2)*math.Sin(dp/2) + math.Cos(p1)*math.Cos(p2)*math.Sin(dl/2)*math.Sin(dl/2)
	return 2 * earthRadius * math.Asin(math.Sqrt(a))
}

// Haversine is exported for other packages (search filters, evaluation).
func Haversine(lat1, lon1, lat2, lon2 float64) float64 { return haversine(lat1, lon1, lat2, lon2) }

// metresPerDegree returns the length of one degree of latitude and longitude.
func metresPerDegree(lat float64) (mLat, mLon float64) {
	return 110574, 111320 * math.Cos(lat*math.Pi/180)
}

// FormatLatLonZH writes a position the way Chinese answers and search text
// expect, with hemisphere words instead of signs: "北纬33.73322°、西经118.33468°".
func FormatLatLonZH(lat, lon float64, prec int) string {
	ns, ew := "北纬", "东经"
	if lat < 0 {
		ns, lat = "南纬", -lat
	}
	if lon < 0 {
		ew, lon = "西经", -lon
	}
	return fmt.Sprintf("%s%.*f°、%s%.*f°", ns, prec, lat, ew, prec, lon)
}
