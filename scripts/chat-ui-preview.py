"""Deterministic browser QA against the real Nuxt pages; all API calls are intercepted.
Run Nuxt on 127.0.0.1:3128 with an empty test DB and no provider keys, then run
python scripts/chat-ui-preview.py. Requires Python Playwright and Chromium.
"""
import json
import os
import sys
from pathlib import Path
from playwright.sync_api import sync_playwright

OUT = Path(os.environ.get('CHAT_PREVIEW_OUT', '/tmp/humanengine-chat-preview'))
OUT.mkdir(parents=True, exist_ok=True)
BASE = os.environ.get('CHAT_PREVIEW_URL', 'http://127.0.0.1:3128')
SCENES = [
    dict(id='ob:s:synthetic-group-one', kind='group', adapterId='snowluma', accountId='100001', platformSceneId='200001', name='夜航开发组', avatarUrl='https://fixture.invalid/group.svg', participationEnabled=False, learningEnabled=False, proactiveEnabled=False),
    dict(id='ob:s:synthetic-group-two', kind='group', adapterId='snowluma', accountId='100001', platformSceneId='200002', name='周末游戏小队', participationEnabled=False, learningEnabled=False, proactiveEnabled=False),
    dict(id='ob:s:synthetic-private-one', kind='private', adapterId='snowluma', accountId='100001', platformSceneId='300001', name='林小雨', avatarUrl='https://fixture.invalid/person.svg', participationEnabled=False, learningEnabled=False, proactiveEnabled=False),
    dict(id='ob:s:synthetic-private-two', kind='private', adapterId='snowluma', accountId='100001', platformSceneId='300002', name='阿哲', participationEnabled=False, learningEnabled=False, proactiveEnabled=False),
]
def message(mid, sender, name, text, minute, **extra):
    return dict(id=mid, sceneId=SCENES[0]['id'], senderId=sender, senderDisplayName=name, sentAt=f'2026-10-28T10:{minute}:00Z', receivedAt=f'2026-10-28T10:{minute}:00Z', origin='history', status='available', parts=[dict(type='text', text=text)], **extra)
GROUP = [
    message('m1', 'ob:u:synthetic-alice', '林小雨', '昨晚的改动已经整理好了，大家有空帮我看看。', '21', senderAvatarUrl='https://fixture.invalid/person.svg', senderPlatformId='300001'),
    message('m2', 'ob:u:synthetic-bob', '阿哲', '看到了！会话列表现在直接显示群名，舒服多了。', '22'),
    message('m3', 'bot', 'HumanEngine', '我在。群聊记录和仿真对话会分开放，不会串到真实会话里。', '22'),
    message('m4', 'ob:u:synthetic-alice', '林小雨', '还有头像和群昵称，这样才知道谁在说话。', '23', senderAvatarUrl='https://fixture.invalid/person.svg'),
    message('m5', 'ob:u:synthetic-other-alice', '林小雨', '同名也没关系，点头像可以看各自的资料。', '24', senderAvatarUrl='https://fixture.invalid/broken.svg', senderPlatformId='300009'),
    message('m6', 'ob:u:synthetic-bob', '阿哲', '那我去试试手机上的布局 👀', '25', replyToId='m4'),
]
PRIVATE = [message('p1', 'ob:u:synthetic-alice', '林小雨', '周末有空一起看看新界面吗？', '30', senderAvatarUrl='https://fixture.invalid/person.svg'), message('p2', 'bot', 'HumanEngine', '当然。这里是私聊历史，只能查看，不会从这个页面发出消息。', '31')]
SIM = [message('s1', 'simulation-user', '你', '你好，帮我测试一下新的聊天界面。', '35'), message('s2', 'bot', 'HumanEngine', '你好！这里是独立的仿真对话。\n你可以放心测试，我不会把消息发送到任何真实群聊或联系人。', '35')]
requests = []
state = {'error': False, 'empty': False}

def api(route):
    req = route.request
    path = req.url.split('/api/', 1)[1]
    if path.startswith('_nuxt_icon/'):
        route.continue_(); return
    requests.append((req.method, path))
    if path == 'auth/session': data = {'authenticated': True}
    elif path == 'scenes': data = SCENES
    elif path.startswith('scenes/') and path.endswith('/history'):
        if state['error']:
            route.fulfill(status=503, json={'error': '暂时无法读取聊天记录，请稍后重试。'}); return
        data = {'items': [] if state['empty'] else (PRIVATE if 'private' in path else GROUP), 'coverage': 'partial', 'reason': None}
    elif path.endswith('/state'): data = {'activeTasks': 0, 'secondsSinceBotSpoke': None, 'topics': []}
    elif path == 'simulations': data = {'id': 'synthetic-simulation'}
    elif path.startswith('simulations/'):
        data = {'ok': True} if req.method == 'DELETE' else SIM
    else:
        raise AssertionError(f'Unexpected API call: {req.method} {path}')
    route.fulfill(json=data)

def avatar(route):
    if 'broken' in route.request.url: route.fulfill(status=404); return
    color = '#557d75' if 'person' in route.request.url else '#676f9b'
    route.fulfill(content_type='image/svg+xml', body=f'<svg xmlns="http://www.w3.org/2000/svg" width="80" height="80"><rect width="80" height="80" rx="24" fill="{color}"/><circle cx="40" cy="30" r="14" fill="#f7e3ca"/><path d="M12 80Q12 48 40 48Q68 48 68 80" fill="#d9e5ce"/><path d="M24 28Q22 10 40 10Q60 10 56 32Q45 14 24 28" fill="#373c4c"/></svg>')

if len(sys.argv) == 3 and sys.argv[1] == '--export-fixture':
    Path(sys.argv[2]).write_text(json.dumps({'SCENES': SCENES, 'GROUP': GROUP, 'PRIVATE': PRIVATE, 'SIM': SIM}, ensure_ascii=False, indent=2))
    raise SystemExit(0)

with sync_playwright() as p:
    browser = p.chromium.launch(executable_path='/usr/bin/chromium', args=['--no-sandbox'])
    context = browser.new_context(viewport={'width': 1440, 'height': 960}, locale='zh-CN', color_scheme='light')
    context.add_cookies([{'name': 'i18n_redirected', 'value': 'zh-CN', 'url': BASE}])
    context.route('**/api/**', api)
    context.route('https://fixture.invalid/**', avatar)
    page = context.new_page()
    errors = []
    page.on('pageerror', lambda error: errors.append(str(error)))
    page.goto(BASE + '/chat')
    page.get_by_role('button', name='夜航开发组', exact=False).wait_for()
    page.get_by_role('button', name='夜航开发组', exact=False).click()
    page.get_by_text('昨晚的改动已经整理好了，大家有空帮我看看。', exact=True).wait_for()
    page.screenshot(path=str(OUT / '01-group.png'), full_page=True)
    assert 'ob:u:' not in page.locator('body').inner_text()
    assert page.locator('textarea').count() == 0
    page.get_by_role('button', name='查看 林小雨 的资料', exact=True).last.click()
    page.get_by_text('300009', exact=True).wait_for()
    page.keyboard.press('Escape')
    page.get_by_role('button', name='林小雨 查看聊天记录', exact=False).click()
    page.get_by_text('周末有空一起看看新界面吗？', exact=True).wait_for()
    page.screenshot(path=str(OUT / '02-private.png'), full_page=True)
    page.get_by_role('button', name='对话（仿真）', exact=False).click()
    page.locator('textarea').fill('你好，帮我测试一下新的聊天界面。')
    page.get_by_role('button', name='发送', exact=True).click()
    page.get_by_text('你好！这里是独立的仿真对话。', exact=False).wait_for()
    page.screenshot(path=str(OUT / '03-simulation.png'), full_page=True)
    assert sum(method == 'POST' and path.endswith('/messages') for method, path in requests) == 1
    # Navigation guard preserves the active simulation until explicit discard.
    page.get_by_role('link', name='场景', exact=True).click()
    page.get_by_role('dialog').wait_for()
    page.get_by_role('button', name='取消', exact=True).click()
    assert page.url.endswith('/chat')
    # A fresh page avoids discarding state just to capture scene names.
    scene_page = context.new_page()
    scene_page.goto(BASE + '/scenes')
    scene_page.get_by_text('夜航开发组', exact=True).first.wait_for()
    scene_page.screenshot(path=str(OUT / '04-scenes.png'), full_page=True)
    # Mobile uses the same list, not a second group picker.
    page.set_viewport_size({'width': 390, 'height': 844})
    page.get_by_role('button', name='返回会话列表', exact=True).click()
    page.screenshot(path=str(OUT / '05-mobile-list.png'), full_page=True)
    page.get_by_role('button', name='夜航开发组', exact=False).click()
    page.screenshot(path=str(OUT / '06-mobile-group.png'), full_page=True)
    assert page.evaluate('document.documentElement.scrollWidth <= innerWidth')
    # Read-only refresh failure, retry, and empty state.
    state['error'] = True
    page.get_by_role('button', name='刷新', exact=True).click()
    page.get_by_text('暂时无法读取聊天记录，请稍后重试。', exact=True).wait_for()
    state['error'] = False; state['empty'] = True
    page.get_by_role('button', name='刷新', exact=True).click()
    page.get_by_text('可获取的历史范围内没有返回消息。', exact=True).wait_for(timeout=3000)
    assert not errors, errors
    (OUT / 'requests.json').write_text(json.dumps(requests, ensure_ascii=False, indent=2))
    browser.close()
print(f'PASS: production chat/scenes fixture screenshots and safety checks: {OUT}')
