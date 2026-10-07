import { useState, type FormEvent } from 'react'
import { Navigate, useNavigate } from 'react-router-dom'
import { useAuth } from '../auth/AuthProvider'
import { ApiError } from '../api/client'
import { IconCloud, IconEye, IconEyeOff, IconLock } from '../components/icons'

export default function LoginPage() {
  const { login, status } = useAuth()
  const navigate = useNavigate()
  const [password, setPassword] = useState('')
  const [showPassword, setShowPassword] = useState(false)
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)

  async function handleSubmit(e: FormEvent) {
    e.preventDefault()
    if (submitting || !password) return
    setSubmitting(true)
    setError('')
    try {
      await login(password)
      navigate('/inbox', { replace: true })
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '网络连接失败，请检查服务状态')
    } finally {
      setSubmitting(false)
    }
  }

  if (status === 'authenticated') return <Navigate to="/inbox" replace />
  if (status === 'checking') return <p className="empty-state" aria-busy="true">加载中…</p>

  return (
    <div className="login-page">
      <div className="login-shell">
        <div className="login-brand">
          <span className="logo" aria-hidden="true"><IconCloud size={31} /></span>
          <p className="login-eyebrow">iCloud MAIL</p>
          <h1>邮件工作区</h1>
          <p>集中管理隐藏邮箱，安静地处理每一封来信。</p>
          <div className="login-pills" aria-label="工作区特性"><span>隐藏邮箱</span><span>主题搜索</span><span>安全读取</span></div>
        </div>
        <form onSubmit={handleSubmit} className="card login-form">
          <div className="login-form-heading"><span>管理台</span><h2>欢迎回来</h2><p>使用管理员密码进入你的工作区</p></div>
          {error && <div className="alert-error" role="alert">{error}</div>}
          <div className="form-field">
            <label htmlFor="admin-password">管理员密码</label>
            <div className="password-input-wrap">
              <span className="password-input-icon" aria-hidden="true"><IconLock size={16} /></span>
              <input id="admin-password" type={showPassword ? 'text' : 'password'} autoComplete="current-password"
                value={password} onChange={(event) => setPassword(event.target.value)} required
                placeholder="输入管理员密码" aria-describedby="admin-password-help" />
              <button type="button" className="password-toggle ghost" onClick={() => setShowPassword((value) => !value)} aria-label={showPassword ? '隐藏密码' : '显示密码'} title={showPassword ? '隐藏密码' : '显示密码'} aria-pressed={showPassword}>
                {showPassword ? <IconEyeOff size={17} /> : <IconEye size={17} />}
              </button>
            </div>
            <p className="hint" id="admin-password-help">使用此工作区的管理员密码，不是 Apple 账号密码。</p>
          </div>
          <div className="form-actions"><button type="submit" className="primary" disabled={submitting}>{submitting ? '登录中…' : '进入工作区'}</button></div>
        </form>
      </div>
      <span className="login-footer">iCloud Mail · 隐私邮箱管理</span>
    </div>
  )
}
