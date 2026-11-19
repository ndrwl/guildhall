// list.js: the task list's live refresh (docs/web.md#the-task-list), inlined on / only. It GETs /list.json at once and
// then every 20 s while the tab is visible, and redraws the band, the stats strip (handing the charts' days to
// charts.js through the charts section's data-days) and the rows in place from the
// page's listparts template with createElement and textContent, moving rows between groups and tinting a
// changed one blue for about 2 s (not under prefers-reduced-motion). It keeps each group's fold, the filter and the
// sort through every refresh, filters and sorts the rows in the browser over their data-sort-* attributes, and keeps
// the URL in step with history.replaceState, so a reload gives the same view from the server. It rewrites every
// <time>'s text from its datetime every 30 s and after each refresh with Intl.RelativeTimeFormat, never its title.
// After a 503, a 500 or a network failure it keeps the rows, says "Not refreshed since …" in the band and tries again
// in 60 s. It never reloads the page and never submits anything; with scripts off the server's page stands alone.
// It is inlined in the page's head, so it waits for the document to be parsed before it looks for the list.
(function start() {
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', start)
    return
  }
  var main = document.querySelector('main.tri')
  var parts = document.getElementById('listparts')
  if (!main || !parts || !parts.content || !window.fetch || !window.history || !window.URLSearchParams) {
    return
  }
  var strip = document.getElementById('stats')
  var box = main.querySelector('.fq')
  var form = main.querySelector('form.fb')
  var sorts = ['task', 'pri', 'stage', 'pr', 'time']
  var sortable = {'g-plan': true, 'g-build': true, 'g-ci': true, 'g-held': true}
  var marks = {ok: '✓', run: '●', bad: '✗'}
  var calm = !!(window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches)
  var rtf = window.Intl && window.Intl.RelativeTimeFormat ? new window.Intl.RelativeTimeFormat('en-GB', {numeric: 'always', style: 'short'}) : null
  var state = readQuery(window.location.search)
  var closed = {}
  var loaded = false
  var failed = false
  var busy = false
  var lastRead = null
  var timer = 0
  var clock = 0

  // readQuery is the filter and sort of a URL's query, as the server reads it: an unknown value is dropped.
  function readQuery(search) {
    var v = new URLSearchParams(search)
    var p = v.get('p') || ''
    var s = v.get('sort') || ''
    return {q: (v.get('q') || '').trim(), p: /^p[0-5]$/.test(p) ? p : '', repo: v.get('repo') || '',
      sort: sorts.indexOf(s) >= 0 ? s : '', desc: v.get('dir') === 'desc'}
  }

  // href is the list's URL for st, its keys in the server's order and the empty ones left out.
  function href(st) {
    var v = new URLSearchParams()
    if (st.desc) { v.set('dir', 'desc') }
    if (st.p) { v.set('p', st.p) }
    if (st.q) { v.set('q', st.q) }
    if (st.repo) { v.set('repo', st.repo) }
    if (st.sort) { v.set('sort', st.sort) }
    var s = v.toString()
    return s ? '/?' + s : '/'
  }

  function withState(change) {
    var c = {q: state.q, p: state.p, repo: state.repo, sort: state.sort, desc: state.desc}
    for (var k in change) {
      c[k] = change[k]
    }
    return c
  }

  function el(tag, cls, text) {
    var e = document.createElement(tag)
    if (cls) { e.className = cls }
    if (text !== undefined) { e.textContent = text }
    return e
  }

  function txt(s) {
    return document.createTextNode(s)
  }

  function part(selector) {
    return parts.content.querySelector(selector).cloneNode(true)
  }

  // safe is u when it is a path on this site or an http or https URL, and otherwise "".
  function safe(u) {
    return typeof u === 'string' && (/^\/(?![\/\\])/.test(u) || /^https?:\/\//i.test(u)) ? u : ''
  }

  // rel is a time's text relative to now, the server's words: "now", "6 min ago", "2 hr ago", "3 days ago", and past
  // 30 days the date its exact-time title names, "4 Oct", with its year when not this one; fallback when it cannot.
  function rel(at, title, fallback) {
    var d = Date.now() - Date.parse(at)
    if (isNaN(d)) { return fallback }
    if (d < 60000) { return 'now' }
    if (!rtf) { return fallback }
    if (d < 3600000) { return rtf.format(-Math.floor(d / 60000), 'minute') }
    if (d < 86400000) { return rtf.format(-Math.floor(d / 3600000), 'hour') }
    var days = Math.floor(d / 86400000)
    if (days <= 30) { return rtf.format(-days, 'day') }
    var w = String(title || '').split(' ') // "Thu 5 Nov 2026, " then the clock and the zone
    if (w.length < 4) { return fallback }
    var year = w[3].replace(',', '')
    return w[1] + ' ' + w[2] + (year === String(new Date().getFullYear()) ? '' : ' ' + year)
  }

  function stamp(t, ft) {
    t.setAttribute('datetime', ft.at)
    t.setAttribute('title', ft.title)
    t.textContent = rel(ft.at, ft.title, ft.title)
  }

  function tick() {
    clearTimeout(clock)
    document.querySelectorAll('time[datetime]').forEach(function (t) {
      t.textContent = rel(t.getAttribute('datetime'), t.getAttribute('title'), t.textContent)
    })
    clock = setTimeout(tick, 30000)
  }

  function key(li) {
    return li.getAttribute('data-sort-repo') + '/' + li.getAttribute('data-sort-id')
  }

  // sig is what a row shows, its time by its instant, so a row whose cells are unchanged is kept as it is.
  function sig(li) {
    var out = [li.className.replace(/\s*\btint\b/g, '')]
    for (var c = li.firstElementChild; c; c = c.nextElementSibling) {
      out.push(c.tagName === 'TIME' ? c.getAttribute('datetime') : c.textContent)
    }
    return out.join('\u0000')
  }

  function tint(li) {
    if (calm) { return }
    li.classList.add('tint')
    setTimeout(function () { li.classList.remove('tint') }, 2000)
  }

  // row is the feed row r drawn as the server draws it.
  function row(r) {
    var li = part('li.r')
    li.className = 'r' + (r.you ? ' you' : '') + (r.front ? ' front' : '')
    li.setAttribute('data-sort-repo', r.repository || '')
    li.setAttribute('data-sort-id', r.id || '')
    li.setAttribute('data-sort-pri', String(r.priority || 0))
    li.setAttribute('data-sort-step', r.sort_step || '')
    li.setAttribute('data-sort-att', String(r.attempt || 0))
    li.setAttribute('data-sort-pr', r.pr ? String(r.pr.number) : '')
    li.setAttribute('data-sort-time', r.since ? r.since.at : '')
    var qp = li.querySelector('.qp')
    if (r.position) { qp.textContent = String(r.position) } else { qp.remove() }
    var a = li.querySelector('.tid')
    var rp = a.querySelector('.rp')
    var id = r.id || ''
    var name = r.name || id
    a.setAttribute('href', safe(r.href))
    if (name.length > id.length) { rp.textContent = name.slice(0, name.length - id.length) } else { rp.remove() }
    a.appendChild(txt(id))
    var rt = li.querySelector('.rt')
    rt.textContent = r.title || ''
    rt.setAttribute('title', r.title || '')
    var pri = li.querySelector('.pri')
    if (r.pri) {
      pri.className = 'pri ' + r.pri
      pri.textContent = r.pri
    } else {
      pri.removeAttribute('class')
    }
    var stp = li.querySelector('.stp')
    if (!r.held) { stp.querySelector('.hm').remove() }
    if (r.step) { stp.querySelector('b').textContent = r.step } else { stp.querySelector('b').remove() }
    if (r.rest) { stp.appendChild(txt(r.rest)) }
    var sx = li.querySelector('.sx')
    if (r.slice) { sx.appendChild(txt(r.slice)) } else { sx.replaceChildren() }
    var prc = li.querySelector('.prc')
    if (r.pr) {
      var pa = prc.querySelector('a')
      var ci = prc.querySelector('.ci')
      if (safe(r.pr.href)) {
        pa.setAttribute('href', safe(r.pr.href))
        pa.textContent = '#' + r.pr.number
      } else {
        pa.replaceWith(txt('#' + r.pr.number))
      }
      if (marks[r.pr.ci]) {
        ci.className = 'ci ' + r.pr.ci
        ci.setAttribute('title', r.pr.ci_word || '')
        ci.textContent = marks[r.pr.ci]
      } else {
        ci.remove()
      }
    } else {
      prc.replaceChildren()
    }
    var t = li.querySelector('time')
    if (r.since) { stamp(t, r.since) } else { t.replaceWith(el('span')) }
    var ak = li.querySelector('.ak')
    if (r.ask) { ak.textContent = r.ask } else { ak.remove() }
    return li
  }

  // fill puts rows in ul, keeping a row whose cells are unchanged and tinting one that changed, moved or is new.
  function fill(ul, rows, old) {
    var lis = rows.map(function (r) {
      var li = row(r)
      var was = old[key(li)]
      if (was && was.parentNode === ul && sig(was) === sig(li)) {
        return was
      }
      if (loaded) { tint(li) }
      return li
    })
    arrange(ul, lis)
  }

  // arrange makes lis the children of ul in order, moving only those out of place, so a kept row stays attached
  // and keeps the keyboard focus.
  function arrange(ul, lis) {
    var at = ul.firstElementChild
    lis.forEach(function (li) {
      while (at && lis.indexOf(at) < 0) {
        var gone = at
        at = at.nextElementSibling
        gone.remove()
      }
      if (li === at) {
        at = at.nextElementSibling
      } else {
        ul.insertBefore(li, at)
      }
    })
    while (at) {
      var next = at.nextElementSibling
      at.remove()
      at = next
    }
  }

  function needs(rows, old) {
    var sec = document.getElementById('g-you')
    if (!rows.length) {
      if (sec) { sec.remove() }
      return
    }
    if (!sec) {
      sec = part('section.you')
      if (strip) { strip.after(sec) } else { main.prepend(sec) }
    }
    sec.querySelector('h2 .n').textContent = String(rows.length)
    fill(sec.querySelector('ul.rows'), rows, old)
  }

  // group is the feed group g's <details>, the one on the page, keeping its fold, or a new one.
  function group(g, old) {
    var d = document.getElementById(g.id)
    if (!d || d.tagName !== 'DETAILS') {
      d = part('details.sg')
      d.id = g.id
      var shut = closed[g.id]
      if (shut === undefined ? g.id === 'g-done' : shut) { d.removeAttribute('open') }
    }
    d.className = 'sg' + (g.id === 'g-done' ? ' fin' : g.moving ? ' mv' : '')
    var h2 = d.querySelector('summary h2')
    var n = h2.querySelector('.n')
    h2.replaceChildren(txt(g.title + ' '), n)
    d.querySelector('summary .sgm').textContent = g.status || ''
    d.querySelectorAll(':scope > p.hold').forEach(function (p) { p.remove() })
    var ul = d.querySelector(':scope > ul.rows');
    (g.banners || []).forEach(function (b) {
      var p = part('p.hold')
      p.querySelector('b').textContent = b.front
      p.appendChild(txt(' ' + b.wait))
      if (b.since) {
        var t = el('time')
        stamp(t, b.since)
        p.appendChild(txt(' since '))
        p.appendChild(t)
      }
      p.appendChild(txt('.'))
      d.insertBefore(p, ul)
    })
    fill(ul, g.rows || [], old)
    return d
  }

  // place puts the groups in order before the notices and the footer, and drops the groups the feed no longer has.
  function place(want) {
    main.querySelectorAll(':scope > details.sg').forEach(function (d) {
      if (want.indexOf(d) < 0) { d.remove() }
    })
    var anchor = main.querySelector(':scope > p.empty, :scope > p.asof')
    for (var i = want.length - 1; i >= 0; i--) {
      if (want[i].nextElementSibling !== anchor || want[i].parentNode !== main) { main.insertBefore(want[i], anchor) }
      anchor = want[i]
    }
  }

  function band(f) {
    var ws = document.querySelector('.band .ws')
    if (ws) {
      ws.className = 'ws' + (f.you ? ' you' : '')
      ws.querySelector('b').textContent = f.count
      var t = el('time')
      stamp(t, f.read_at)
      ws.querySelector('.m').replaceChildren(txt(f.meta + ' · updated '), t)
    }
    var asof = main.querySelector(':scope > p.asof time')
    if (asof) { stamp(asof, f.read_at) }
  }

  function stats(x) {
    if (!strip || !x || !x.text) { return }
    var t = x.text
    var c = strip.querySelectorAll('.stc')
    if (c.length !== 3) { return }
    var w = c[0].querySelectorAll('li')
    var s = c[1].querySelectorAll('li')
    var m = c[2].querySelectorAll('li')
    var day = c[1].querySelector('h2 .sd')
    if (w.length !== 4 || s.length !== 3 || m.length !== 3 || !day) { return }
    strip.querySelector('summary').textContent = t.summary
    w[0].replaceChildren(el('b', '', t.running))
    w[1].textContent = t.start_queue
    w[2].replaceChildren(txt(t.merge_queue));
    (t.fronts || []).forEach(function (fr, i) {
      var a = el('a', '', fr.name)
      a.setAttribute('href', safe(fr.href))
      w[2].appendChild(txt(i ? ', ' : ' · front '))
      w[2].appendChild(a)
    })
    w[3].textContent = t.merged
    day.textContent = t.day
    s[0].replaceChildren(el('b', '', t.cost))
    if (t.cost_note) {
      s[0].appendChild(txt(' '))
      s[0].appendChild(el('span', 'sd', t.cost_note))
    }
    s[1].textContent = t.attempts
    s[2].textContent = t.tokens
    m[0].replaceChildren(el('b', '', t.load), txt(' · ' + t.cores))
    m[1].textContent = t.memory
    m[2].textContent = t.disk
    // the charts' days, which charts.js watches and redraws from
    var charts = document.getElementById('charts')
    if (charts && Array.isArray(t.days) && t.days.length) {
      var days = JSON.stringify(t.days)
      if (charts.getAttribute('data-days') !== days) { charts.setAttribute('data-days', days) }
    }
  }

  function cmpStr(x, y) {
    return x < y ? -1 : x > y ? 1 : 0
  }

  function num(x) {
    return Number(x) || 0
  }

  function attr(li, name) {
    return li.getAttribute('data-sort-' + name) || ''
  }

  // since orders by time in state, oldest first, an unknown one last, then by repository and id.
  function since(a, b) {
    var x = attr(a, 'time')
    var y = attr(b, 'time')
    if (!x && y) { return 1 }
    if (x && !y) { return -1 }
    return cmpStr(x, y) || cmpStr(attr(a, 'repo'), attr(b, 'repo')) || cmpStr(attr(a, 'id'), attr(b, 'id'))
  }

  // order is the server's sort of a stage group's rows: by default priority, then oldest in state; a row with no
  // value for the sort comes last either way.
  function order(a, b) {
    var dir = state.desc ? -1 : 1
    switch (state.sort) {
      case 'task':
        return dir * (cmpStr(attr(a, 'repo'), attr(b, 'repo')) || cmpStr(attr(a, 'id'), attr(b, 'id')))
      case 'stage':
        return dir * (cmpStr(attr(a, 'step'), attr(b, 'step')) || num(attr(a, 'att')) - num(attr(b, 'att'))) || since(a, b)
      case 'pr':
        if (!attr(a, 'pr') && !attr(b, 'pr')) { return since(a, b) }
        if (!attr(a, 'pr')) { return 1 }
        if (!attr(b, 'pr')) { return -1 }
        return dir * (num(attr(a, 'pr')) - num(attr(b, 'pr'))) || since(a, b)
      case 'time':
        if (!attr(a, 'time') !== !attr(b, 'time')) { return since(a, b) }
        return dir * since(a, b)
    }
    return dir * (num(attr(a, 'pri')) - num(attr(b, 'pri'))) || since(a, b)
  }

  // matches says whether a row passes the filter: its priority, repository, and id or title.
  function matches(li) {
    if (state.p && state.p !== 'p' + attr(li, 'pri')) { return false }
    if (state.repo && state.repo !== attr(li, 'repo')) { return false }
    if (!state.q) { return true }
    var needle = state.q.toLowerCase()
    var title = li.querySelector('.rt')
    return key(li).toLowerCase().indexOf(needle) >= 0 || (!!title && title.textContent.toLowerCase().indexOf(needle) >= 0)
  }

  function chip(label, count, st, on, name, value) {
    var a = el('a', 'cp' + (on ? ' on' : ''), label)
    a.setAttribute('href', href(st))
    a.setAttribute('data-' + name, value)
    if (count) { a.appendChild(el('i', '', String(count))) }
    return a
  }

  // chips redraws the priority and repository chips from the open rows, each toggling its value.
  function chips() {
    var pri = {}
    var repo = {}
    var repos = []
    var open = 0
    main.querySelectorAll('#g-you li.r, :scope > details.sg:not(#g-done) li.r').forEach(function (li) {
      var r = attr(li, 'repo')
      open++
      pri['p' + attr(li, 'pri')] = (pri['p' + attr(li, 'pri')] || 0) + 1
      if (!repo[r]) { repos.push(r) }
      repo[r] = (repo[r] || 0) + 1
    })
    var fp = document.getElementById('fp')
    if (fp) {
      var ps = [fp.firstElementChild, chip('All', 0, withState({p: ''}), !state.p, 'p', '')]
      for (var i = 0; i <= 5; i++) {
        var l = 'p' + i
        if (pri[l] || state.p === l) {
          ps.push(chip(l, pri[l] || 0, withState({p: state.p === l ? '' : l}), state.p === l, 'p', l))
        }
      }
      fp.replaceChildren.apply(fp, ps)
    }
    var fr = document.getElementById('fr')
    if (fr) {
      var rs = [fr.firstElementChild, chip('All', open, withState({repo: ''}), !state.repo, 'repo', '')]
      repos.forEach(function (r) {
        rs.push(chip(r, repo[r], withState({repo: state.repo === r ? '' : r}), state.repo === r, 'repo', r))
      })
      fr.replaceChildren.apply(fr, rs)
    }
  }

  // sortLinks marks the sort in effect on the column header and the phone's chips, each reversing it when pressed.
  function sortLinks() {
    main.querySelectorAll('a[data-sort]').forEach(function (a) {
      var k = a.getAttribute('data-sort')
      var on = state.sort === k || (!state.sort && k === 'pri')
      var c = withState({sort: k, desc: on && !state.desc})
      if (!c.desc && k === 'pri') { c.sort = '' }
      a.setAttribute('href', href(c))
      var label = a.getAttribute('data-label')
      if (label !== null) {
        a.className = 'cp' + (on ? ' on' : '')
        a.textContent = label + (on ? (state.desc ? ' ▴' : ' ▾') : '')
      } else {
        a.className = on ? (state.desc ? 'on desc' : 'on') : ''
      }
    })
  }

  function notice(any, shown) {
    main.querySelectorAll(':scope > p.empty').forEach(function (p) { p.remove() })
    var p = null
    if ((state.q || state.p || state.repo) && shown === 0) {
      var a = el('a', '', 'Show every task')
      a.setAttribute('href', href({q: '', p: '', repo: '', sort: state.sort, desc: state.desc}))
      a.setAttribute('data-clear', '')
      p = el('p', 'empty', 'No task matches the filter. ')
      p.appendChild(a)
    } else if (!any) {
      p = el('p', 'empty', 'No task is open, and none finished in the last 24 hours.')
    }
    if (p) { main.insertBefore(p, main.querySelector(':scope > p.asof')) }
  }

  // view applies the filter and sort to the rows on the page and puts them in the URL. Needs you is never filtered,
  // and the merge queue and Recently finished keep their own order.
  function view() {
    var shown = 0
    var any = !!document.getElementById('g-you')
    main.querySelectorAll(':scope > details.sg').forEach(function (d) {
      var ul = d.querySelector(':scope > ul.rows')
      var lis = Array.prototype.slice.call(ul.children)
      var n = 0
      any = true
      if (sortable[d.id]) {
        lis.sort(order)
        arrange(ul, lis)
      }
      lis.forEach(function (li) {
        li.hidden = !matches(li)
        if (!li.hidden) { n++ }
      })
      d.hidden = n === 0
      d.querySelector('summary .n').textContent = String(n)
      shown += n
    })
    chips()
    sortLinks()
    notice(any, shown)
    var u = href(state)
    if (window.location.pathname + window.location.search !== u) {
      window.history.replaceState(window.history.state, '', u)
    }
  }

  function render(f) {
    if (!f || !Array.isArray(f.groups) || !f.read_at) { throw new Error('not a task list') }
    var old = {}
    main.querySelectorAll('li.r').forEach(function (li) { old[key(li)] = li })
    band(f)
    stats(f.stats)
    needs(f.needs || [], old)
    var want = f.groups.map(function (g) { return group(g, old) })
    if (f.finished) { want.push(group(f.finished, old)) }
    place(want)
    lastRead = f.read_at
    loaded = true
    view()
    tick()
  }

  function showStale() {
    var s = document.getElementById('lag')
    if (!s) { return }
    s.hidden = !failed
    if (failed && lastRead) { stamp(s.querySelector('time'), lastRead) }
  }

  function schedule(ms) {
    clearTimeout(timer)
    if (document.visibilityState !== 'hidden') { timer = setTimeout(poll, ms) }
  }

  function poll() {
    clearTimeout(timer)
    if (busy) { return }
    busy = true
    window.fetch('/list.json', {credentials: 'same-origin', cache: 'no-store', redirect: 'error'})
      .then(function (res) {
        if (!res.ok) { throw new Error('status ' + res.status) }
        return res.json()
      })
      .then(function (f) {
        render(f)
        failed = false
        return 20000
      })
      .catch(function () {
        failed = true
        return 60000
      })
      .then(function (ms) {
        busy = false
        showStale()
        tick()
        schedule(ms)
      })
  }

  main.addEventListener('toggle', function (e) {
    var d = e.target
    if (d && d.tagName === 'DETAILS' && d.id) { closed[d.id] = !d.hasAttribute('open') }
  }, true)

  main.addEventListener('click', function (e) {
    if (!loaded || e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || !e.target.closest) {
      return
    }
    var a = e.target.closest('a[data-p], a[data-repo], a[data-sort], a[data-clear]')
    if (!a) { return }
    e.preventDefault()
    state = readQuery(new URL(a.getAttribute('href'), document.baseURI).search)
    if (box) { box.value = state.q }
    view()
  })

  if (form && box) {
    form.addEventListener('submit', function (e) {
      if (!loaded) { return }
      e.preventDefault()
      state.q = box.value.trim()
      view()
    })
    box.addEventListener('input', function () {
      if (!loaded) { return }
      state.q = box.value.trim()
      view()
    })
  }

  document.addEventListener('visibilitychange', function () {
    if (document.visibilityState === 'visible') {
      poll()
    } else {
      clearTimeout(timer)
    }
  })

  if (strip && window.matchMedia && window.matchMedia('(max-width: 760px)').matches) {
    strip.removeAttribute('open')
  }
  tick()
  poll()
})()
