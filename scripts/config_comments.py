"""当前公共配置与私有覆盖的中文字段说明。"""
import re

COMMENTS = {'tencent': '腾讯云原生COS与CI配置；凭据只写私有YAML。',
'bucket': '专用私有桶完整名称（含APPID）；必须从未开启版本控制。',
'region': '腾讯云桶地域，例如ap-shanghai。',
'secretid': '腾讯云最小权限子账号SecretId，仅私有YAML。',
'secretkey': '腾讯云SecretKey，仅私有YAML。',
'sessiontoken': '可选临时凭据Token，到期前更新并重启。',
'biztype': 'CI已绑定桶的审核策略标识。',
'objecturl': '可选COS专用HTTPS代理源站；通常留空使用官方地址。',
'imageurl': '可选CI专用HTTPS代理源站；通常留空使用官方地址。',
'ca': '可选专用代理CA完整PEM，内联私有YAML。',
'masterkey': '唯一应用主密钥：32字节密码学随机值的Base64；程序按环境和用途派生子密钥。所有进程保持一致，已有数据时禁止直接更换。',
'environment': '环境：development开发、production生产；不从环境变量推断。',
'bootstrap': '仅显式初始化使用；普通API/worker启动不迁移、不建账号。',
'login': '首次初始化的后台登录名；修改此值不会自动重置已有账号。',
'server': 'HTTP监听与生命周期；端口暴露范围由address决定。',
'address': '监听或连接地址（host:port）；按当前配置块填写。',
'readtimeout': '读取HTTP请求的总超时，带单位，例如15s。',
'writetimeout': '写入HTTP响应的总超时，带单位，例如30s。',
'shutdowntimeout': '收到TERM后等待请求结束的上限，必须为正。',
'database': '唯一数据库配置；API、后台任务、显式迁移使用同一账号。',
'driver': '数据库驱动只支持postgres；foundation未启用数据库时可省略。',
'maxopen': '该连接池最大连接数；原生小规格实例建议2。',
'maxidle': '最大空闲连接数，不能大于maxopen。',
'maxlifetime': '连接复用寿命，默认30m。',
'redis': '共享限流连接；正式业务必须启用鉴权。',
'username': '命名ACL用户；空值使用default用户。',
'password': '原口令直接写入私有YAML，建议加引号；不读取口令文件或环境变量。',
'db': 'Redis数据库编号，默认0；不同环境仍须独立实例。',
'log': '技术日志设置；不得把正文、口令或DSN写入日志。',
'level': '日志等级，例如info、warn、error。',
'kind': '业务进程类型：card/content/whispers/account/evidence。',
'runtime': '业务任务、外部对象存储与删除恢复的共用设置。',
'enabled': '显式启用当前配置块；关闭不会自动清理已有数据。',
'localtest': '仅开发允许本地供应商适配；正式环境必须false。',
'command': '业务事务期限、锁等待和幂等回执设置。',
'worker': '当前进程的默认任务职责。',
'tasks': '数据库持久任务的扫描、消费、分析和整次操作预算。',
'consumer': '消费者事务和重试边界；失败任务仍受持久状态约束。',
'timeout': '单次事务/操作总超时；时长必须带单位，例如5s。',
'locktimeout': '数据库锁等待上限；不得大于该事务Timeout。',
'receiptttl': '幂等回执保留期，示例168h（7天）。',
'maxattempts': '该层操作最大尝试次数；耗尽后不能静默丢弃任务。',
'retrydelay': '重试前等待时长；带单位，例如1m。',
'analysistimeout': '一次内容分析调用超时；需容纳在外层操作期限内。',
'operationtimeout': '整次工作处理上限；应大于内部事务与供应商调用预算。',
'providertimeout': '一次供应商HTTP调用超时；带单位。',
'wechatappid': '真实小程序AppID；不使用示例用户绕过微信身份校验。',
'wechatsecret': '原微信AppSecret，内联单行字符串；至少16字符。',
'adminpassword': '首次初始化超级管理员的口令，至少12字符。',
'admincreatepassword': '新建后台账号的初始口令，至少12字符，必须与首次超管口令不同。',
'sessionttl': '学生会话有效期；必须带单位，例如24h。',
'exchangettl': '微信交换临时凭证有效期；例如1m。',
'adminsessionttl': '后台账号会话有效期；例如8h。',
'loginwindow': '共享登录限流时间窗口；例如1m。',
'loginlimit': '一个窗口内允许的登录尝试次数。',
'maxtextbytes': '业务文本字节上限，不是字符数。',
'card': '学生卡图片解码、工作租约及重试约束。',
'maxbytes': '上传图片最大字节数；最高8MiB。',
'maxpixels': '图片最大像素总数；限制解码内存。',
'decodeslots': '单进程并发解码槽位；原生小规格建议1。',
'lease': '工作租约时长；必须覆盖内部事务预算。',
'objectdirectory': '本地对象数据目录；相对configs目录解析，不在启动时删除或更换历史对象。',
'scaninterval': '持久任务补偿扫描间隔。',
'mode': 'foundation仅健康探针；development为开发装配；business为完整业务；whispers为开发独立树洞。',
'host': 'PostgreSQL单个主机名或IP，不含端口；IPv6不加方括号，不支持多主机列表。',
'port': 'PostgreSQL端口，整数1至65535；默认5432。',
'name': 'PostgreSQL数据库名；须由管理员预先创建。',
'user': '专用业务库账号，拥有本库对象；禁止超级用户、BYPASSRLS和集群管理权限。',
'sslmode': 'TLS模式：disable关闭；allow/prefer允许明文回退；require仅加密；verify-ca校验证书链；verify-full同时校验主机名，生产跨主机建议verify-full（系统信任根）。',
'timezone': '数据库会话时区，默认UTC；也可填写Asia/Shanghai等有效时区。',
'connecttimeout': '数据库建立连接超时，1s至60s的整秒时长；区别于连接复用寿命。',
'business': '完整业务配置；业务权限通过账号会话、岗位及领域函数检查。',
'journalfile': '加密删除意图账本；相对configs目录解析，父目录0700，不能随业务数据库一并回滚。',
'resourcesfile': '已核实公开资料的YAML路径；相对configs目录解析。',
'wordlistsfile': '初始化词库数据YAML；相对configs目录解析，普通启动不发布。',
'whispers': '开发独立树洞设置；正式业务读取business.runtime。',
}

def annotate(text):
    """只对YAML映射键加注释，跳过多行文本块，保持配置值逐字不变。"""
    result, block_indent = [], None
    for line in text.splitlines():
        indent = len(line) - len(line.lstrip())
        if block_indent is not None:
            if not line.strip() or indent > block_indent:
                result.append(line)
                continue
            block_indent = None
        match = re.match(r'^(\s*)([A-Za-z][A-Za-z0-9_]*):(?:\s|$)', line)
        if match:
            note = COMMENTS.get(match[2].lower())
            if note:
                result.append(match[1] + '# ' + note)
            if re.search(r': [|>][-+]?\s*$', line):
                block_indent = indent
        result.append(line)
    return '\n'.join(result) + '\n'
