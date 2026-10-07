import { useState, type FormEvent } from 'react'
import { Navigate, useNavigate } from 'react-router-dom'
import { useAuth } from '../auth/AuthProvider'
import { ApiError } from '../api/client'
import { IconCloud, IconLock } from '../components/icons'

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
      <div className="login-brand">
        <span className="logo" aria-hidden="true"><IconCloud size={32} /></span>
        <h1>iCloud Mail</h1>
        <p>你的隐藏邮箱与邮件工作区</p>
      </div>
      <form onSubmit={handleSubmit} className="card login-form">
        <h2>登录工作区</h2>
        {error && <div className="alert-error" role="alert">{error}</div>}
        <div className="form-field">
          <label htmlFor="admin-password">管理员密码</label>
          <div className="password-input-wrap">
            <span className="password-input-icon" aria-hidden="true"><IconLock size={16} /></span>
            <input id="admin-password" type={showPassword ? 'text' : 'password'} autoComplete="current-password"
              value={password} onChange={(event) => setPassword(event.target.value)} required
              placeholder="输入管理员密码" aria-describedby="admin-password-help" />
            <button type="button" className="password-toggle ghost" onClick={() => setShowPassword((value) => !value)} aria-pressed={showPassword}>
              {showPassword ? '隐藏' : '显示'}密码
            </button>
          </div>
          <p className="hint" id="admin-password-help">使用此工作区的管理员密码，不是 Apple 账号密码。</p>
        </div>
        <div className="form-actions"><button type="submit" className="primary" disabled={submitting}>{submitting ? '登录中…' : '登录'}</button></div>
      </form>
      <span className="login-footer">iCloud 隐藏邮箱 · 专注收件与管理</span>
    </div>
  )
}
