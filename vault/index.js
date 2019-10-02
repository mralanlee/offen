var router = require('./src/router')
var handler = require('./src/handler')
var allowsCookies = require('./src/allows-cookies')
var consentStatus = require('./src/user-consent')
var getSessionId = require('./src/session-id')

if (!window.fetch) {
  require('unfetch/polyfill')
}

if (!window.URL || !window.URLSearchParams) {
  require('url-polyfill')
}

var register = router()

register('EVENT', optInMiddleware, eventDuplexerMiddleware, anonymousMiddleware, function (event, respond, next) {
  console.log(__('This page is using offen to collect usage statistics.'))
  console.log(__('You can access and manage all of your personal data or opt-out at "%s/auditorium/".', window.location.origin))
  console.log(__('Find out more about offen at "https://www.offen.dev".'))
  handler.handleAnalyticsEvent(event.data)
    .catch(next)
})

register('QUERY', sameOriginMiddleware, callHandler(handler.handleQuery))
register('OPTIN_STATUS', sameOriginMiddleware, callHandler(handler.handleOptinStatus))
register('OPTIN', sameOriginMiddleware, callHandler(handler.handleOptin))
register('PURGE', sameOriginMiddleware, callHandler(handler.handlePurge))
register('LOGIN', sameOriginMiddleware, callHandler(handler.handleLogin))
register('CHANGE_CREDENTIALS', sameOriginMiddleware, callHandler(handler.handleChangeCredentials))
register('FORGOT_PASSWORD', sameOriginMiddleware, callHandler(handler.handleForgotPassword))
register('RESET_PASSWORD', sameOriginMiddleware, callHandler(handler.handleResetPassword))

module.exports = register

function optInMiddleware (event, respond, next) {
  var status = consentStatus()
  if (!status) {
    var expires = new Date(Date.now() + 10 * 365 * 24 * 60 * 60 * 1000)
    status = window.confirm('Are you ok with us collecting usage data?')
      ? 'allow'
      : 'deny'
    document.cookie = 'consent=' + status + '; expires="' + expires.toUTCString() + '"; path=/'
  }

  if (status === 'allow') {
    return next()
  }
  console.log('This page is using offen to collect usage statistics.')
  console.log('You have opted out of data collection, no data is being collected.')
  console.log('Find out more about offen at "https://www.offen.dev".')
}

function eventDuplexerMiddleware (event, respond, next) {
  // eventDuplexerMiddleware adds properties to an event that could be subject to spoofing
  // or unwanted access by 3rd parties in "script". For example adding the session id
  // here instead of the script prevents other scripts from reading this value.
  var now = new Date()
  if (!allowsCookies()) {
    event.data.anoynmous = true
    event.data.payload.event = {
      timestamp: now,
      type: event.data.payload.event.type
    }
    return next()
  }
  Object.assign(event.data.payload.event, {
    timestamp: now,
    sessionId: getSessionId(event.data.payload.accountId)
  })
  next()
}

function sameOriginMiddleware (event, respond, next) {
  if (event.origin !== window.location.origin) {
    return next(new Error('Incoming message had untrusted origin "' + event.origin + '", will not process.'))
  }
  next()
}

function anonymousMiddleware (event, respond, next) {
  if (!event.data.anoynmous) {
    return next()
  }
  console.log(__('This page is using offen to collect usage statistics.'))
  console.log(__('Your setup prevents or you have disabled third party cookies in your browser\'s settings.'))
  console.log(__('Basic usage data will be collected anonymously.'))
  console.log(__('Find out more at "%s".', window.location.origin))
  handler.handleAnonymousEvent(event.data)
    .catch(next)
}

function callHandler (handler) {
  return function (event, respond, next) {
    Promise.resolve(handler(event.data))
      .then(respond, next)
  }
}
