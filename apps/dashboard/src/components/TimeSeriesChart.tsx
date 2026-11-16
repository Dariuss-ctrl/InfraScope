import type { XAXisComponentOption, YAXisComponentOption } from 'echarts'
import { LineChart, type LineSeriesOption } from 'echarts/charts'
import {
  GridComponent,
  type GridComponentOption,
  LegendComponent,
  type LegendComponentOption,
  MarkLineComponent,
  type MarkLineComponentOption,
  TooltipComponent,
  type TooltipComponentOption,
} from 'echarts/components'
// ECharts' `use` registers chart parts; it is not a React hook.
import { type ComposeOption, type EChartsType, init, use as register } from 'echarts/core'
import { SVGRenderer } from 'echarts/renderers'
import { useEffect, useRef, useState } from 'react'
import type { Point } from '../api/services.ts'
import { toLineData } from '../chartData.ts'

// Only the parts drawn here are bundled.
register([LineChart, GridComponent, LegendComponent, MarkLineComponent, TooltipComponent, SVGRenderer])

type Option = ComposeOption<
  | LineSeriesOption
  | GridComponentOption
  | LegendComponentOption
  | MarkLineComponentOption
  | TooltipComponentOption
  | XAXisComponentOption
  | YAXisComponentOption
>

// ChartMarker is a moment worth seeing against the series, such as a
// deployment: a dashed vertical line with a short label. color names a CSS
// custom property, like ChartSeries.color.
export type ChartMarker = { t: Date; label: string; color: string }

// color names a CSS custom property, such as --chart-p95, so charts follow
// the page's light or dark theme.
export type ChartSeries = { name: string; points: readonly Point[]; color?: string }

type Props = {
  label: string
  series: readonly ChartSeries[]
  from: Date
  to: Date
  format: (value: number | null) => string
  // Called with the time under the pointer when the plot area is clicked.
  onSelect?: (t: Date) => void
  // Drawn as vertical lines; they take no clicks, so a click still selects
  // the point under it.
  markers?: readonly ChartMarker[]
}

// TimeSeriesChart draws step-aligned series on a fixed time axis, so a gap
// in the data shows as a gap instead of a line drawn across it.
export function TimeSeriesChart({ label, series, from, to, format, onSelect, markers = noMarkers }: Props) {
  const container = useRef<HTMLDivElement>(null)
  const chart = useRef<EChartsType | null>(null)
  const select = useRef(onSelect)
  const dark = useDarkScheme()

  useEffect(() => {
    select.current = onSelect
  })

  useEffect(() => {
    const element = container.current
    if (!element) {
      return
    }
    const instance = init(element, null, { renderer: 'svg' })
    chart.current = instance
    const surface = instance.getZr()
    surface.on('click', (event) => {
      const pixel = [event.offsetX, event.offsetY]
      if (select.current && instance.containPixel('grid', pixel)) {
        const [t] = instance.convertFromPixel('grid', pixel) as number[]
        select.current(new Date(t))
      }
    })
    surface.on('mousemove', (event) => {
      const clickable =
        select.current !== undefined && instance.containPixel('grid', [event.offsetX, event.offsetY])
      surface.setCursorStyle(clickable ? 'pointer' : 'default')
    })
    const resize = new ResizeObserver(() => instance.resize())
    resize.observe(element)
    return () => {
      resize.disconnect()
      instance.dispose()
      chart.current = null
    }
  }, [])

  useEffect(() => {
    // Colours come from the page's tokens; `dark` re-reads them when the
    // system switches theme.
    const theme = getComputedStyle(document.documentElement)
    const token = (name: string) => theme.getPropertyValue(name).trim()
    const muted = token('--muted')
    const border = token('--border')
    const axisText = { color: muted, fontSize: 11 }
    const option: Option = {
      animation: false,
      textStyle: { fontFamily: token('--font-sans') },
      grid: { left: 60, right: 12, top: series.length > 1 ? 30 : 12, bottom: 22 },
      legend:
        series.length > 1
          ? { top: 0, right: 0, icon: 'roundRect', itemWidth: 10, itemHeight: 3, textStyle: axisText }
          : undefined,
      tooltip: {
        trigger: 'axis',
        backgroundColor: token('--surface'),
        borderColor: token('--border-strong'),
        textStyle: { color: token('--text'), fontSize: 12 },
        valueFormatter: (value) => format(typeof value === 'number' ? value : null),
      },
      xAxis: {
        type: 'time',
        min: from.getTime(),
        max: to.getTime(),
        axisLabel: { ...axisText, hideOverlap: true },
        axisLine: { lineStyle: { color: border } },
        axisTick: { show: false },
      },
      yAxis: {
        type: 'value',
        min: 0,
        axisLabel: { ...axisText, formatter: (value: number) => format(value) },
        splitLine: { lineStyle: { color: border, type: 'dashed' } },
      },
      series: series.map((s, i) => ({
        name: s.name,
        type: 'line',
        color: s.color ? token(s.color) : undefined,
        symbolSize: 5,
        lineStyle: { width: 1.75 },
        // A single series gets a faint fill, which reads as volume.
        areaStyle: series.length === 1 ? { opacity: 0.1 } : undefined,
        data: toLineData(s.points),
        // The markers hang off the first series, once per chart.
        markLine:
          i === 0 && markers.length > 0
            ? {
                silent: true,
                symbol: 'none',
                animation: false,
                label: { formatter: '{b}', position: 'end', distance: 2, color: muted, fontSize: 10 },
                data: markers.map((m) => ({
                  name: m.label,
                  xAxis: m.t.getTime(),
                  lineStyle: { color: token(m.color), type: 'dashed', width: 1.25 },
                })),
              }
            : undefined,
      })),
    }
    chart.current?.setOption(option, { notMerge: true })
  }, [series, from, to, format, dark, markers])

  return <div ref={container} className="chart" role="img" aria-label={label} />
}

// A stable empty default, so a chart without markers does not redraw on
// every render.
const noMarkers: readonly ChartMarker[] = []

// useDarkScheme follows the system's light or dark setting, which the page's
// colour tokens follow too.
function useDarkScheme(): boolean {
  const query = '(prefers-color-scheme: dark)'
  const [dark, setDark] = useState(() => window.matchMedia(query).matches)
  useEffect(() => {
    const media = window.matchMedia(query)
    const update = () => setDark(media.matches)
    media.addEventListener('change', update)
    return () => media.removeEventListener('change', update)
  }, [])
  return dark
}
