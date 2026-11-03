"""当前公共配置与私有覆盖的中文字段说明。"""
import re

COMMENTS = {'masterkey': '唯一应用主密钥：32字节密码学随机值的Base64；程序按环境和用途派生子密钥。所有进程保持一致，已有数据时禁止直接更换。',
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
'redis': '共享限流与缓存连接；正式业务必须启用鉴权。',
'username': '命名ACL用户；空值使用default用户。',
'password': '原口令直接写入私有YAML，建议加引号；不读取口令文件或环境变量。',
'db': 'Redis数据库编号，默认0；不同环境仍须独立实例。',
'group': '基础探针生产者组名。',
'log': '技术日志设置；不得把正文、口令或DSN写入日志。',
'level': '日志等级，例如info、warn、error。',
'kind': '业务进程类型：card/content/whispers/account/evidence。',
'contentanalyzer': '原文复核模式；空值或manual，词库命中与显式重审均交人工。',
'privacyscope': '隐私工作范围：account账号删除，evidence证据清理。',
'runtime': '业务任务、外部对象存储与删除恢复的共用设置。',
'enabled': '显式启用当前配置块；关闭不会自动清理已有数据。',
'localtest': '仅开发允许本地供应商适配；正式环境必须false。',
'databasemaxopen': '每个领域连接池上限；原生小规模为2，总连接数还需计入进程数。',
'databasemaxidle': '每个领域连接池最大空闲连接数；原生小规模为1。',
'gatewayurl': '可选外部对象网关HTTPS地址（供应商待确定）；不得含账号、查询参数或片段。',
'gatewaytoken': '网关Bearer令牌，内联单行字符串；至少16字符，不记录到日志。',
'gatewayca': '可选私有CA的完整PEM；多行用 | 块。空值使用系统信任根。',
'providerversion': '对象服务协议版本；仅在配置对象服务时填写。',
'command': '业务事务期限、锁等待和幂等回执设置。',
'worker': '当前进程类型或消费者参数；以本文件所属层级为准。',
'consumer': '消费者事务和重试边界；失败任务仍受持久状态约束。',
'timeout': '单次事务/操作总超时；时长必须带单位，例如5s。',
'locktimeout': '数据库锁等待上限；不得大于该事务Timeout。',
'receiptttl': '幂等回执保留期，示例168h（7天）。',
'maxattempts': '该层操作最大尝试次数；耗尽后不能静默丢弃任务。',
'retrydelay': '重试前等待时长；带单位，例如1m。',
'analysistimeout': '一次内容分析调用超时；需容纳在外层操作期限内。',
'interval': '任务扫描/调度间隔；带单位，例如1s。',
'operationtimeout': '整次工作处理上限；应大于内部事务与供应商调用预算。',
'providertimeout': '一次供应商HTTP调用超时；带单位。',
'wechatappid': '真实小程序AppID；不使用示例用户绕过微信身份校验。',
'wechatsecret': '原微信AppSecret，内联单行字符串；至少16字符。',
'adminpassword': '后台初始化原口令，内联单行字符串；至少12字符，不自动重置账号。',
'initialpassword': '独立后台初始化原口令；至少12字符。',
'bootstraplogin': '独立后台显式初始化登录名；普通启动不创建账号。',
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
'topic': '专用业务主题；首次部署显式创建。',
'eventtype': '允许的领域事件类型，必须与对应业务消费者匹配。',
'producergroup': '发件箱生产者组名。',
'consumergroup': '消费者组名；不同环境/用途不得复用。',
'allowunauthenticated': '仅隔离集成可为true；生产必须false。',
'concurrency': '单进程最大消费并发数。',
'relay': '事务发件箱转发参数。',
'databasetimeout': '发件箱数据库操作超时。',
'leaseduration': '发件箱转发租约时长。',
'relayinterval': '发件箱扫描间隔。',
'scaninterval': '持久任务补偿扫描间隔。',
'parameters': '中间件原生参数映射；脚本从这里生成只读运行输入。',
'authentication': 'PG访问规则按顺序匹配；仅本机允许，保留受限LOGIN边界。',
'acl': 'Redis用户ACL列表；保存原口令哈希，与API私有口令对应。',
'output': '自动生成的第三方原生文件名；不要直接维护生成文件。',
'format': '第三方输出格式：text为原生文本，json为JSON；唯一维护来源仍是本YAML。',
'content': '目标工具要求的原生文本；以多行块保持可读性。',
'mode': 'foundation仅健康探针；development为本地适配器；business为完整正式业务。',
'host': 'PostgreSQL单个主机名或IP，不含端口；IPv6不加方括号，不支持多主机列表。',
'port': 'PostgreSQL端口，整数1至65535；默认5432。',
'name': 'PostgreSQL数据库名；须由管理员预先创建。',
'user': '专用业务库账号，拥有本库对象；禁止超级用户、BYPASSRLS和集群管理权限。',
'sslmode': 'TLS模式：disable关闭；allow/prefer允许明文回退；require仅加密；verify-ca校验证书链；verify-full同时校验主机名，生产跨主机建议verify-full（系统信任根）。',
'timezone': '数据库会话时区，默认UTC；也可填写Asia/Shanghai等有效时区。',
'connecttimeout': '数据库建立连接超时，1s至60s的整秒时长；区别于连接复用寿命。',
'business': '完整业务配置；业务权限通过账号会话、岗位及领域函数检查。',
'development': '开发本地适配器，不代替真实微信、审核、通知或对象存储验收。',
'journalfile': '加密删除意图账本；相对configs目录解析，父目录0700，不能随业务数据库一并回滚。',
'catalogfile': '已核实公开资料的YAML路径；相对configs目录解析。',
'wordlistsfile': '初始化词库数据YAML；相对configs目录解析，普通启动不发布。',
'whispers': '开发独立树洞设置；正式业务读取business.runtime。',
'providerversion': '对象服务协议版本；仅在配置对象服务时填写。'}

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
