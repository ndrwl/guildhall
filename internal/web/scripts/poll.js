// poll.js: the new-reply bar (docs/web.md#the-new-reply-bar). It asks the page's own since route, every 5 s while
// the tab is visible and every 60 s while it is hidden, whether the Director has written since the page loaded, and
// whether the server restarted since, which makes the page's form token out of date. It shows the bar, fills it with
// textContent, links Reload to the talk and counts new messages in the tab's title; it never reloads the page and
// never submits anything. The bar's data-* attributes are its only input; on a page with no bar it does nothing.
(function () {
  var bar = document.getElementById('newbar')
  if (!bar || !window.fetch) {
    return
  }
  var since = bar.getAttribute('data-since')
  var base = bar.getAttribute('data-base')
  var last = bar.getAttribute('data-last')
  var start = bar.getAttribute('data-start')
  var title = document.title
  var news = bar.querySelector('.nbm')
  var restarted = bar.querySelector('.nbs')
  var timer = 0
  function schedule() {
    clearTimeout(timer)
    timer = setTimeout(poll, document.visibilityState === 'hidden' ? 60000 : 5000)
  }
  function show(r) {
    if (r.start !== start) {
      restarted.hidden = false
      bar.hidden = false
    }
    if (r.new > 0 && typeof r.tag === 'string') {
      bar.querySelector('.nbt').textContent = '#' + r.tag
      bar.querySelector('.nbw').textContent = r.at || ''
      bar.querySelector('.nbr').setAttribute('href', base + '?seen=' + encodeURIComponent(String(r.last)) + '#' + encodeURIComponent(r.tag) + '-talk')
      news.hidden = false
      bar.hidden = false
      document.title = '(' + r.new + ') ' + title
    }
  }
  function poll() {
    window.fetch(since + '?after=' + encodeURIComponent(last), {credentials: 'same-origin', cache: 'no-store', redirect: 'error'})
      .then(function (res) { return res.ok ? res.json() : null })
      .then(function (r) { if (r) { show(r) } })
      .catch(function () {})
      .then(schedule)
  }
  document.addEventListener('visibilitychange', function () {
    if (document.visibilityState === 'visible') {
      poll()
    } else {
      schedule()
    }
  })
  schedule()
})()
