// charts.js: the stats strip's two 14-day charts (docs/web.md#the-task-list), inlined on / only, after the vendored
// uPlot (internal/web/vendor/uplot). It reads the days from the charts section's data-days, which the server writes
// and list.js rewrites after each refresh, draws merges per day as bars and spend per day as a line, and shows the
// section, which the page serves hidden so that with scripts off the charts are left out. Hovering over or tapping a
// day writes its words, "5 Nov · 4 merges · $38", under the charts with textContent; with no day under the cursor
// they name the last day. There is no zoom and no pan. It makes no request and builds no HTML from a string.
(function start() {
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', start)
    return
  }
  var box = document.getElementById('charts')
  var U = window.uPlot
  if (!box || typeof U !== 'function' || !U.paths || !U.paths.bars || !window.JSON) {
    return
  }
  var holders = [document.getElementById('ch-merges'), document.getElementById('ch-spend')]
  var readout = box.querySelector('.chr')
  var strip = document.getElementById('stats')
  if (!holders[0] || !holders[1] || !readout) {
    return
  }
  var days = []
  var plots = []

  // read is the days of data-days, or none when it is not a list.
  function read() {
    try {
      var d = JSON.parse(box.getAttribute('data-days') || '[]')
      return Array.isArray(d) ? d : []
    } catch (e) {
      return []
    }
  }

  function colour(name, fallback) {
    var v = window.getComputedStyle(document.documentElement).getPropertyValue(name)
    return v && v.trim() ? v.trim() : fallback
  }

  function columns() {
    var x = days.map(function (_, i) { return i })
    return [
      [x, days.map(function (d) { return d.merges })],
      [x, days.map(function (d) { return typeof d.cost === 'number' ? d.cost : null })]
    ]
  }

  // show writes day i's words, or the last day's when no day is under the cursor.
  function show(i) {
    var d = i === null || i === undefined ? days[days.length - 1] : days[i]
    readout.textContent = d ? d.text : ''
  }

  function opts(which, width) {
    var mut = colour('--mut', '#5b6270')
    var line = colour('--line', '#e2e5ea')
    var run = colour('--run', '#1d6fe0')
    var font = '11px system-ui, sans-serif'
    var whole = which === 0
    var yAxis = {
      stroke: mut, font: font, size: 44, space: 24,
      grid: {stroke: line, width: 1}, ticks: {show: false},
      values: function (u, splits) {
        return splits.map(function (v) { return whole ? String(v) : '$' + (v >= 10 ? Math.round(v) : +v.toFixed(2)) })
      }
    }
    if (whole) {
      yAxis.incrs = [1, 2, 5, 10, 20, 50, 100, 200, 500]
    }
    return {
      width: width,
      height: 120,
      legend: {show: false},
      scales: {
        x: {time: false, range: function () { return [-0.5, days.length - 0.5] }},
        y: {range: function (u, min, max) { return [0, Math.max(whole ? 1 : 0.01, max || 0)] }}
      },
      cursor: {
        drag: {x: false, y: false, setScale: false},
        sync: {key: 'guildhall-days'},
        points: {show: !whole}
      },
      select: {show: false},
      axes: [
        {
          stroke: mut, font: font, size: 24, space: 36, incrs: [1, 2, 7],
          grid: {show: false}, ticks: {stroke: line},
          values: function (u, splits) {
            return splits.map(function (v) { return days[v] ? days[v].label : '' })
          }
        },
        yAxis
      ],
      series: [
        {},
        whole
          ? {label: 'Merges', stroke: run, fill: run, width: 0, paths: U.paths.bars({size: [0.6, 28]}), points: {show: false}}
          : {label: 'Spend', stroke: run, width: 2, points: {show: true, size: 5, fill: run}}
      ],
      hooks: {setCursor: [function (u) { show(u.cursor.idx) }]}
    }
  }

  // tap moves the cursor to the day under a touch, for phones whose tap gives no mousemove.
  function tap(u) {
    u.over.addEventListener('touchstart', function (e) {
      var t = e.touches && e.touches[0]
      if (!t) { return }
      var r = u.over.getBoundingClientRect()
      u.setCursor({left: t.clientX - r.left, top: t.clientY - r.top})
    }, {passive: true})
  }

  function width(h) {
    return Math.max(0, Math.floor(h.clientWidth))
  }

  function destroy() {
    plots.forEach(function (u) { u.destroy() })
    plots = []
  }

  // build draws both charts afresh at their holders' widths, once the strip is open and has room.
  function build() {
    destroy()
    if (!days.length) {
      box.hidden = true
      return
    }
    box.hidden = false
    show(null)
    var data = columns()
    holders.forEach(function (h, i) {
      var w = width(h)
      if (!w) { return }
      var u = new U(opts(i, w), data[i], h)
      tap(u)
      plots.push(u)
    })
  }

  // update takes new days: the same number redraws the data in place, so a hover survives a refresh.
  function update() {
    var next = read()
    var same = next.length === days.length && plots.length === holders.length
    days = next
    if (!same) {
      build()
      return
    }
    var data = columns()
    plots.forEach(function (u, i) { u.setData(data[i]) })
    show(null)
  }

  function resize() {
    if (plots.length !== holders.length) {
      build()
      return
    }
    plots.forEach(function (u, i) {
      var w = width(holders[i])
      if (w) { u.setSize({width: w, height: 120}) }
    })
  }

  days = read()
  build()
  if (window.MutationObserver) {
    new window.MutationObserver(update).observe(box, {attributes: true, attributeFilter: ['data-days']})
  }
  window.addEventListener('resize', resize)
  if (strip) {
    strip.addEventListener('toggle', function () {
      if (strip.open) { resize() }
    })
  }
  if (window.matchMedia) {
    var dark = window.matchMedia('(prefers-color-scheme: dark)')
    if (dark.addEventListener) {
      dark.addEventListener('change', build)
    }
  }
})()
