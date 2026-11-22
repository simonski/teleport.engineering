"use strict";

function bindGoogleLogin() {
  const section=document.querySelector('#google-login'),form=document.querySelector('#login');
  const url=new URL(window.location.href),outcome=url.searchParams.get('google');
  const screens={
    waitlist:['You’re on the waitlist','An administrator must approve your Google account before you can enter Teleport. Return to sign in after approval.'],
    registered:['Registration required','This workspace is open to registered users. An administrator can review your access request.'],
    unavailable:['Google sign-in is unavailable','Try again later or sign in with your Teleport username and password.'],
    failed:['Google sign-in could not be completed','Start a new Google sign-in or use your Teleport username and password.']
  };
  if(outcome!==null){
    url.searchParams.delete('google');
    history.replaceState(history.state,'',url.pathname+url.search+url.hash);
  }
  const screen=Object.hasOwn(screens,outcome)?screens[outcome]:null;
  if(screen){
    const title=document.createElement('h1'),description=document.createElement('p');
    title.textContent=screen[0];description.textContent=screen[1];section.append(title,description);
    if(outcome==='waitlist'||outcome==='registered'){
      form.hidden=true;
      const back=document.createElement('button');
      back.type='button';back.className='secondary';back.textContent='Back to sign in';
      back.onclick=()=>{section.replaceChildren();form.hidden=false;form.querySelector('input').focus();loadGoogleOption();};
      section.append(back);back.focus();
      return;
    }
  }
  loadGoogleOption();

  async function loadGoogleOption(){
    try{
      const settings=await api('getAuthCapabilities');
      if(!section.isConnected||!settings.google)return;
      const link=document.createElement('a');
      const desktop=desktopAuthorization();
      link.className='google-sign-in';link.href='/auth/google/start'+(desktop?'?'+desktop.toString():'');
      const badge=document.createElement('img');
      badge.src='/google-sign-in.png';badge.alt='Sign in with Google';badge.width=180;badge.height=40;
      link.append(badge);
      section.append(link);
    }catch(error){if(!error.staleInteraction&&section.isConnected&&!screen)notice('Google sign-in status is unavailable.');}
  }
}

function desktopAuthorization(){
  const query=new URL(window.location.href).searchParams;
  const port=query.get('desktop_port'),state=query.get('desktop_state'),challenge=query.get('desktop_challenge');
  const secret=/^[A-Za-z0-9_-]{43}$/;
  if(!/^[1-9][0-9]{0,4}$/.test(port)||Number(port)>65535||!secret.test(state)||!secret.test(challenge))return null;
  const fields=new URL('https://localhost').searchParams;
  fields.set('desktop_port',port);fields.set('desktop_state',state);fields.set('desktop_challenge',challenge);
  return fields;
}

async function approveDesktopSignIn(request){
  let account;
  try{account=(await api('getSession')).account;}
  catch(error){if(error.status===401||error.status===403){login();return;}throw error;}
  root.innerHTML='<main class="login desktop-authorization"><h1>Connect Teleport</h1><p id="desktop-account"></p><button type="button" id="desktop-approve">Allow sign-in</button><button type="button" class="secondary" id="desktop-switch">Use another account</button><p id="desktop-status" role="status"></p></main>';
  byId('desktop-account').textContent='Authorize the desktop app as '+account.username+'.';
  const approve=byId('desktop-approve'),change=byId('desktop-switch'),status=byId('desktop-status');
  const act=async run=>{
    approve.disabled=change.disabled=true;
    try{await run();}catch(error){status.textContent=error.message;approve.disabled=change.disabled=false;}
  };
  approve.onclick=()=>act(async()=>{
    const result=await api('authorizeClient',{challenge:request.get('desktop_challenge'),accountId:account.id});
    if(!/^[A-Za-z0-9_-]{43}$/.test(result.code))throw new Error('Invalid desktop authorization response');
    const callback=new URL('http://127.0.0.1:'+request.get('desktop_port')+'/teleport/callback');
    callback.searchParams.set('code',result.code);
    callback.searchParams.set('state',request.get('desktop_state'));
    window.location.assign(callback);
  });
  change.onclick=()=>act(async()=>{await api('logout');login();});
}

async function openGoogleSettings(){
  const owner=session?.account.id,generation=loadGeneration,detail=detailEpoch;
  const [initial,queue]=await Promise.all([api('getGoogleSSO'),api('listGoogleEnrollments')]);
  if(!owner||session?.account.id!==owner||!session.account.administrator||generation!==loadGeneration||detail!==detailEpoch)return;
  const dialog=closeOnlyDialog('Google sign-in',`<div class="settings-list"><section>
    <label>OAuth client ID<input id="google-client" maxlength="512" autocomplete="off" value="${escapeHTML(initial.clientId)}"></label>
    <label>OAuth client secret<input id="google-secret" type="password" maxlength="1024" autocomplete="new-password" placeholder="${initial.configured?'Leave blank to keep the saved secret':''}"></label>
    <label>Authorized redirect URI<input id="google-redirect" type="url" maxlength="2048" value="${escapeHTML(initial.redirectUri||location.origin+'/auth/google/callback')}"></label>
    <label>Enrollment mode<select id="google-mode"><option value="auto-enrol">Auto-enrol</option><option value="waitlist">Waitlist</option><option value="registered">Registered</option></select></label>
    <p class="form-help" id="google-mode-help"></p>
    <button type="button" id="google-save">Save configuration</button>
    <button type="button" class="secondary" id="google-toggle"></button>
    <p id="google-settings-status" role="status"></p></section>
    <section><h2>Enrollment queue</h2><button type="button" class="secondary" id="google-refresh">Refresh queue</button><div id="google-enrollments"></div></section></div>`);
  const guard=captureDialogCompletion(dialog),mode=byId('google-mode'),status=byId('google-settings-status'),holder=byId('google-enrollments');
  const save=byId('google-save'),toggle=byId('google-toggle'),secret=byId('google-secret'),refresh=byId('google-refresh');
  let current=initial,busy=false,items=queue.items;
  mode.value=current.mode;
  const descriptions={
    'auto-enrol':'Grant ordinary workspace access automatically when execution is off and existing host work has been reconciled.',
    waitlist:'Tell applicants they are on the waitlist until an administrator approves them.',
    registered:'Show a registration-required message until an administrator approves the Google account.'
  };
  function modeHelp(){byId('google-mode-help').textContent=descriptions[mode.value];}
  function controls(){
    save.disabled=refresh.disabled=busy;
    toggle.disabled=busy||!current.configured;
    toggle.textContent=current.enabled?'Disable SSO':'Enable SSO';
    holder.querySelectorAll('button').forEach(button=>{button.disabled=busy;});
  }
  function renderQueue(){
    holder.replaceChildren();
    if(!items.length){holder.textContent='No Google sign-in requests yet.';return;}
    for(const item of items){
      const row=document.createElement('div'),email=document.createElement('strong'),state=document.createElement('span');
      row.className='google-enrollment';email.textContent=item.email;
      state.textContent=item.suspended?'Suspended':item.administrator?'Administrator':item.accountId?'Enrolled':'Pending';
      row.append(email,state);
      if(!item.suspended&&!item.administrator){
        const button=document.createElement('button');
        const grant=!!item.accountId;
        button.type='button';button.className='secondary';button.textContent=grant?'Make administrator':'Approve enrollment';
        button.onclick=()=>{
          const prompt=grant?'Grant installation administrator access to ':'Approve workspace access for ';
          if(window.confirm(prompt+item.email+'?'))act(grant?'grantGoogleAdministrator':'approveGoogleEnrollment',{id:item.id,confirm:true},result=>{
            items=items.map(row=>row.id===result.id?result:row);renderQueue();
            status.textContent=grant?'Administrator access granted.':'Enrollment approved.';
          });
        };
        row.append(button);
      }
      holder.append(row);
    }
  }
  async function act(operation,input,onSuccess){
    if(busy)return;
    busy=true;controls();status.textContent='';
    try{const result=await api(operation,input);if(guard.current())onSuccess(result);}
    catch(error){if(guard.current())status.textContent=error.message;}
    finally{busy=false;if(guard.current())controls();}
  }
  mode.onchange=modeHelp;
  save.onclick=()=>act('setupGoogleSSO',{
    expectedVersion:current.version,clientId:byId('google-client').value.trim(),clientSecret:secret.value.trim(),
    redirectUri:byId('google-redirect').value.trim(),mode:mode.value
  },result=>{current=result;secret.value='';secret.placeholder='Leave blank to keep the saved secret';status.textContent='Google sign-in configuration saved.';});
  toggle.onclick=()=>act(current.enabled?'disableGoogleSSO':'enableGoogleSSO',{expectedVersion:current.version},result=>{
    current=result;status.textContent=current.enabled?'Google sign-in enabled.':'Google sign-in disabled.';
  });
  refresh.onclick=()=>act('listGoogleEnrollments',{},result=>{items=result.items;renderQueue();});
  modeHelp();renderQueue();controls();
}
