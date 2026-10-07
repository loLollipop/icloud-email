import { Link } from 'react-router-dom'
import { IconAccounts, IconAliases, IconInbox } from '../components/icons'

export default function HelpPage() {
  return (
    <section className="help-page" aria-label="接入指南">
      <div className="page-header"><p className="page-description">三个步骤，连接隐藏邮箱和实际收件邮箱。</p></div>
      <ol className="help-steps">
        <li className="card"><span className="help-step-icon"><IconAccounts /></span><div><h2>添加 iCloud 账号</h2><p>填写账号名称、iCloud 邮箱及对应区域。</p><Link to="/accounts">前往邮箱账户 →</Link></div></li>
        <li className="card"><span className="help-step-icon"><IconAliases /></span><div><h2>连接隐藏邮箱</h2><p>在账号的「连接设置」中更新 Cookie，或使用 iCloud 登录，再查看、创建和管理隐藏邮箱。</p><Link to="/aliases">前往隐藏邮箱 →</Link></div></li>
        <li className="card"><span className="help-step-icon"><IconInbox /></span><div><h2>接入实际转发邮箱</h2><p>在「接入收件邮箱」中填写隐藏邮箱实际转发目标的 IMAP 信息与 App 专用密码。若转发到 Gmail，请接入对应的 Gmail 邮箱；转发到 iCloud 则使用对应的 iCloud 收件凭据。</p><p className="hint">凭据标签只表示已填写配置。连接状态以实际验证与收件结果为准。</p><Link to="/accounts">配置收件邮箱 →</Link><Link to="/inbox">打开收件箱 →</Link></div></li>
      </ol>
    </section>
  )
}
