package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"net/http"
	"os"

	svg "github.com/ajstarks/svgo"
	"github.com/golang/freetype"
	"github.com/golang/freetype/truetype"
	geojson "github.com/paulmach/go.geojson"
	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
)

type IntensityQuery struct {
	ID    int `json:"id"`
	Scale int `json:"scale"`
}

// Function to convert intensity scale to color
func intensityToColor(scale int) string {
	switch scale {
	case 0:
		return "#27272a"
	case 1:
		return "#bae6fd"
	case 2:
		return "#4ade80"
	case 3:
		return "#facc15"
	case 4:
		return "#f97316"
	case 5:
		return "#dc2626"
	case 6:
		return "#86198f"
	case 7:
		return "#500724"
	default:
		if scale > 6 {
			return "#4a044e"
		}
		if scale > 5 {
			return "#b91c1c"
		}
		return "#27272a"
	}
}

func loadFont(weight int) (*truetype.Font, error) {
	var fontPath string
	switch weight {
	case 400:
		fontPath = "./fonts/roboto-regular.ttf"
	case 500:
		fontPath = "./fonts/roboto-medium.ttf"
	default:
		fontPath = "./fonts/roboto-regular.ttf" // default to regular
	}

	fontBytes, err := os.ReadFile(fontPath)
	if err != nil {
		return nil, err
	}
	f, err := freetype.ParseFont(fontBytes)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// Function to calculate the drawing range
func calculateBounds(fc *geojson.FeatureCollection, scaleMap map[int]int) (minLon, minLat, maxLon, maxLat float64) {
	minLon = 180.0
	minLat = 90.0
	maxLon = -180.0
	maxLat = -90.0

	for _, feature := range fc.Features {
		// Skip if the scale is 0 (transparent prefectures are not calculated)
		id := int(feature.Properties["id"].(float64))
		if scaleMap[id] == 0 {
			continue
		}

		// Calculate the range from the coordinates of the polygon
		switch feature.Geometry.Type {
		case "Polygon":
			for _, ring := range feature.Geometry.Polygon {
				for _, coord := range ring {
					lon, lat := coord[0], coord[1]
					minLon = min(minLon, lon)
					minLat = min(minLat, lat)
					maxLon = max(maxLon, lon)
					maxLat = max(maxLat, lat)
				}
			}
		case "MultiPolygon":
			for _, polygon := range feature.Geometry.MultiPolygon {
				for _, ring := range polygon {
					for _, coord := range ring {
						lon, lat := coord[0], coord[1]
						minLon = min(minLon, lon)
						minLat = min(minLat, lat)
						maxLon = max(maxLon, lon)
						maxLat = max(maxLat, lat)
					}
				}
			}
		}
	}
	return
}

func calculateCenter(coords [][]float64) (float64, float64) {
	var sumLon, sumLat float64
	count := len(coords)

	for _, coord := range coords {
		sumLon += coord[0]
		sumLat += coord[1]
	}

	return sumLon / float64(count), sumLat / float64(count)
}

// Function to convert SVG data to PNG
func svgToPNG(svgData []byte, width, height int, footerText string, showScale bool, multiplier float64, features []*geojson.Feature, scaleMap map[int]int, funcToScreen func(float64, float64) (float64, float64)) ([]byte, error) {
	// Loading SVG data
	icon, err := oksvg.ReadIconStream(bytes.NewReader(svgData))
	if err != nil {
		return nil, fmt.Errorf("failed to read icon stream: %w", err)
	}

	// Drawing Area Settings
	icon.SetTarget(0, 0, float64(width), float64(height))

	// Creating RGBA images for drawing
	rgba := image.NewRGBA(image.Rect(0, 0, width, height))
	scanner := rasterx.NewScannerGV(width, height, rgba, rgba.Bounds())
	raster := rasterx.NewDasher(width, height, scanner)

	// SVG rendering
	icon.Draw(raster, 1.0)

	if footerText == "" {
		footerText = "Code available under the MIT License (GitHub: evacuate)."
	}

	// Load the font
	f, err := loadFont(400)
	if err != nil {
		return nil, fmt.Errorf("failed to load font: %w", err)
	}

	// Context for scale value text drawing
	c := freetype.NewContext()
	c.SetDPI(72)
	c.SetFont(f)
	c.SetFontSize(14 * multiplier)
	c.SetClip(rgba.Bounds())
	c.SetDst(rgba)
	c.SetSrc(image.NewUniform(color.RGBA{0xfa, 0xfa, 0xfa, 0xff}))

	if showScale {
		// Scale values are drawn at the center of each prefecture
		for _, feature := range features {
			id := int(feature.Properties["id"].(float64))
			scale, exists := scaleMap[id]
			if !exists || scale == 0 {
				continue
			}

			var centerLon, centerLat float64
			switch feature.Geometry.Type {
			case "Polygon":
				centerLon, centerLat = calculateCenter(feature.Geometry.Polygon[0])
			case "MultiPolygon":
				// Use the center of the first polygon
				centerLon, centerLat = calculateCenter(feature.Geometry.MultiPolygon[0][0])
			}

			// Converted to screen coordinates
			x, y := funcToScreen(centerLon, centerLat)
			pt := freetype.Pt(int(x)-5, int(y)+5)
			_, err = c.DrawString(fmt.Sprintf("%d", scale), pt)
			if err != nil {
				return nil, fmt.Errorf("failed to draw scale value: %w", err)
			}
		}
	}

	pt := freetype.Pt(int(10*multiplier), height-int(14*multiplier))
	_, err = c.DrawString(footerText, pt)
	if err != nil {
		return nil, fmt.Errorf("failed to draw footer text: %w", err)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, rgba); err != nil {
		return nil, fmt.Errorf("failed to encode png: %w", err)
	}
	return buf.Bytes(), nil
}

func mapHandler(w http.ResponseWriter, r *http.Request) {
	scaleData := r.URL.Query().Get("scale")
	if scaleData == "" {
		http.Error(w, "scale parameter is required", http.StatusBadRequest)
		return
	}

	var intensities []IntensityQuery
	if err := json.Unmarshal([]byte(scaleData), &intensities); err != nil {
		http.Error(w, fmt.Sprintf("Invalid scale data format: %v", err), http.StatusBadRequest)
		return
	}

	scaleMap := make(map[int]int)
	for _, intensity := range intensities {
		// Check the intensity value
		if intensity.Scale < 0 || intensity.Scale > 7 {
			http.Error(w, fmt.Sprintf("Invalid scale value for ID %d: %d",
				intensity.ID, intensity.Scale), http.StatusBadRequest)
			return
		}
		scaleMap[intensity.ID] = intensity.Scale
	}

	size := r.URL.Query().Get("size")
	var multiplier float64 = 1.0

	switch size {
	case "1":
		multiplier = 1.0 // 1280x720
	case "2":
		multiplier = 2.0 // 2560x1440
	case "3":
		multiplier = 4.0 // 5120x2880
	default:
		multiplier = 1.0
	}

	const (
		BASE_WIDTH  = 1280.0
		BASE_HEIGHT = 720.0
	)

	CANVAS_WIDTH := BASE_WIDTH * multiplier
	CANVAS_HEIGHT := BASE_HEIGHT * multiplier

	data, err := os.ReadFile("japan.geojson")
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to read geojson: %v", err), http.StatusInternalServerError)
		return
	}

	fc, err := geojson.UnmarshalFeatureCollection(data)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to unmarshal geojson: %v", err), http.StatusInternalServerError)
		return
	}

	// Calculate the valid area
	minLon, minLat, maxLon, maxLat := calculateBounds(fc, scaleMap)

	funcToScreen := func(lon, lat float64) (x, y float64) {
		// Calculate the effective drawing area
		margin := 0.1
		effectiveWidth := CANVAS_WIDTH * (1.0 - 2*margin)
		effectiveHeight := CANVAS_HEIGHT * (1.0 - 2*margin)

		// Calculate center coordinates only once
		centerLat := (maxLat + minLat) / 2
		centerLon := (maxLon + minLon) / 2
		centerX := CANVAS_WIDTH / 2
		centerY := CANVAS_HEIGHT / 2

		// Calculate the correction factor for longitude distance by latitude
		lonCorrection := math.Cos(centerLat * math.Pi / 180.0)

		lonSpan := (maxLon - minLon) * lonCorrection // Correct longitude range
		latSpan := maxLat - minLat

		scaleX := effectiveWidth / lonSpan
		scaleY := effectiveHeight / latSpan
		scale := min(scaleX, scaleY)

		x = ((lon-centerLon)*lonCorrection)*scale + centerX
		y = (centerLat-lat)*scale + centerY
		return
	}

	buf := new(bytes.Buffer)
	canvas := svg.New(buf)
	canvas.Start(int(CANVAS_WIDTH), int(CANVAS_HEIGHT))
	canvas.Rect(0, 0, int(CANVAS_WIDTH), int(CANVAS_HEIGHT), "fill:#18181b")

	for _, feature := range fc.Features {
		id, ok := feature.Properties["id"].(float64)
		if !ok {
			http.Error(w, "Invalid ID format in GeoJSON", http.StatusInternalServerError)
			return
		}

		scaleValue := 0
		if val, ok := scaleMap[int(id)]; ok {
			scaleValue = val
		}
		fillColor := intensityToColor(scaleValue)

		var paths []string
		if feature.Geometry.Type == "Polygon" {
			for _, ring := range feature.Geometry.Polygon {
				var pathStr = "M"
				for i, coord := range ring {
					x, y := funcToScreen(coord[0], coord[1])
					if i == 0 {
						pathStr += fmt.Sprintf("%.1f %.1f", x, y)
					} else {
						pathStr += fmt.Sprintf(" L%.1f %.1f", x, y)
					}
				}
				pathStr += " Z"
				paths = append(paths, pathStr)
			}
		} else if feature.Geometry.Type == "MultiPolygon" {
			for _, polygon := range feature.Geometry.MultiPolygon {
				for _, ring := range polygon {
					var pathStr = "M"
					for i, coord := range ring {
						x, y := funcToScreen(coord[0], coord[1])
						if i == 0 {
							pathStr += fmt.Sprintf("%.1f %.1f", x, y)
						} else {
							pathStr += fmt.Sprintf(" L%.1f %.1f", x, y)
						}
					}
					pathStr += " Z"
					paths = append(paths, pathStr)
				}
			}
		}

		finalPath := ""
		for _, p := range paths {
			finalPath += p + " "
		}

		strokeWidth := 0.4 * multiplier
		style := fmt.Sprintf("fill:%s;stroke:#a1a1aa;stroke-width:%.1f;fill-opacity:0.8",
			fillColor, strokeWidth)
		canvas.Path(finalPath, style)
	}

	footerText := r.URL.Query().Get("footer")
	showScale := r.URL.Query().Get("scale_text") == "true"

	canvas.End()

	// Convert SVG to PNG
	pngData, err := svgToPNG(buf.Bytes(), int(CANVAS_WIDTH), int(CANVAS_HEIGHT), footerText, showScale, float64(multiplier), fc.Features, scaleMap, funcToScreen)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to convert svg to png: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "image/png")
	w.Write(pngData)
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func main() {
	http.HandleFunc("/map", mapHandler)

	log.Println("Starting server on :8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal(err)
	}
}
