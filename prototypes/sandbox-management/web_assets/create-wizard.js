'use strict';

// THROWAWAY: drafts are local to this dialog; only the final command mutates a host.
function captureCreateValues(){
 if(createWizard)createWizard.values={...createWizard.values,...collectFields(createWizard.form,$('create-form'))};
}
function createChains(){return createWizard.form.fields.filter(f=>f.key.startsWith('chain:')&&createWizard.values[f.key]==='true').map(f=>f.key.slice(6));}
function createImage(){
 if(createWizard.mode==='existing')return images().find(i=>i.id===createWizard.imageID);
 const chains=createChains();return images().find(i=>i.toolchains.length===chains.length&&i.toolchains.every(c=>chains.includes(c)));
}
function createImagePreview(){
 const img=createImage(),chains=createWizard.mode==='existing'?(img?.toolchains??[]):createChains();
 if(createWizard.mode==='existing'&&!img)return '<p class="dim">Select an image to inspect its tools.</p>';
 return `<div class="create-preview"><h3>${safe(img?.name??chainLabel(chains))}</h3>${dl([['Image',img?.id??'Built on confirmation'],['Toolchains',chainLabel(chains)],['Platform',img?.platform??'linux/amd64'],['Size',img?.size??'Calculated at build']])}${toolTable(img??{toolchains:chains})}</div>`;
}
function createPayload(skipIntegrations=false){
 const w=createWizard,values={...w.values,name:w.values.name.trim(),workspace:w.values.workspace.trim(),imageMode:w.mode,imageID:w.mode==='existing'?w.imageID:''};
 for(const f of w.form.fields){
  if(f.key.startsWith('chain:')&&w.mode==='existing')values[f.key]='false';
  if(skipIntegrations&&f.key.startsWith('connect:'))values[f.key]='false';
 }
 values.gitName=skipIntegrations?'':values.gitName.trim();values.gitEmail=skipIntegrations?'':values.gitEmail.trim();
 return {action:'create-sandbox',values};
}
function createFields(predicate){
 const fields=createWizard.form.fields.filter(predicate).map(f=>{
  if(f.key.startsWith('agent:')){const a=state.agentCatalog.find(a=>a.id===f.key.slice(6));return {...f,label:a?a.name+' / '+a.version:f.label};}
  if(f.key.startsWith('connect:'))return {...f,label:({github:'Connect GitHub',azure:'Connect Azure',azdo:'Connect Azure DevOps'})[f.key.slice(8)]};
  return f;
 });
 return fieldsHTML({...createWizard.form,fields},createWizard.values,'create-field-');
}
function createReview(){
 const w=createWizard,payload=createPayload(),values=payload.values,img=createImage(),old=state.retained.find(b=>b.name===values.name);
 const agents=w.form.fields.filter(f=>f.key.startsWith('agent:')&&values[f.key]==='true').map(f=>f.key.slice(6));
 const connections=['github','azure','azdo'].filter(key=>values['connect:'+key]==='true');
 const workspace=values.workspace||old?.workspace||'volume:'+values.name+'-workspace';
 return `<div class="create-review"><h3>Review new sandbox</h3>${dl([['Instance',w.instanceName+' / '+w.group],['Name',values.name],['Status after create',values.start==='true'?'running':'stopped'],['Workspace',workspace],['Agents',agents.join(', ')],['Image source',w.mode==='existing'?'Use selected image':img?'Reuse matching image':'Build a new image'],['Image',img?.name??chainLabel(createChains())],['Image ID',img?.id??'Created on confirmation'],['Git name',values.gitName||old?.gitName||'not configured'],['Git email',values.gitEmail||old?.gitEmail||'not configured'],['Connect integrations',connections.join(', ')||'Skipped / configure later']])}${createImagePreview()}${old?'<div class="note">Retained workspace and home data will be reused. The selected agents will replace the saved agent selection; saved integrations are preserved.</div>':''}<div class="note amber">No changes made yet. The final button creates the sandbox and builds or reuses its image on this instance. Connections and image builds are simulated.</div></div>`;
}
function renderCreateWizard(){
 if(!createWizard)return;const w=createWizard,focus=focusToken();
 $('create-scope').textContent=w.instanceName+' / '+w.group;
 $('create-title').textContent=w.step===4?'Sandbox created':'New sandbox';
 $('create-progress').innerHTML=['Image','Sandbox','Integrations','Review'].map((label,n)=>`<li class="${n===w.step?'current':n<w.step?'complete':''}" ${n===w.step?'aria-current="step"':''}><span>${n<w.step?'✓':n+1}</span>${label}${n===2?' (optional)':''}</li>`).join('');
 let body='';
 if(w.step===0){
  body=`<div class="create-modes" role="group" aria-label="Image source"><button id="create-mode-existing" type="button" data-create-mode="existing" aria-pressed="${w.mode==='existing'}" ${images().length?'':'disabled'}><strong>Use an existing image</strong><small>Choose an image available on this instance.</small></button><button id="create-mode-new" type="button" data-create-mode="new" aria-pressed="${w.mode==='new'}"><strong>Create a new image</strong><small>Choose the toolchains to build into the image.</small></button></div>`;
  if(w.mode==='existing')body+=`<div class="create-image-grid"><div class="create-image-list" role="group" aria-label="Available images">${images().map(i=>`<button id="create-image-${safe(i.id)}" type="button" class="create-image" data-create-image="${safe(i.id)}" aria-pressed="${w.imageID===i.id}"><strong>${safe(i.name)}</strong><small>${safe(i.id)} / ${safe(i.size)}</small><small>${safe(chainLabel(i.toolchains))}</small></button>`).join('')}</div>${createImagePreview()}</div>`;
  else body+=`<h3>Choose image toolchains</h3><p class="dim">No selection uses the base image.</p><div class="dialogfields">${createFields(f=>f.key.startsWith('chain:'))}</div>${createImagePreview()}<div class="note">${createImage()?'This toolchain set already exists on this instance. Its image will be reused.':'The image will be built together with the sandbox after your final confirmation.'}</div>`;
 }
 if(w.step===1)body=`<h3>Configure the sandbox</h3><p class="dim">Choose its name, workspace and at least one agent. Versions are illustrative.</p><div class="dialogfields">${createFields(f=>['name','workspace','start'].includes(f.key)||f.key.startsWith('agent:'))}</div><small class="dim">An empty workspace creates a dedicated volume. At least one agent is required.</small>`;
 if(w.step===2){
  const chains=w.mode==='existing'?(createImage()?.toolchains??[]):createChains();
  if(!chains.includes('azure')){w.values['connect:azure']='false';w.values['connect:azdo']='false';}
  body=`<h3>Set up integrations <span class="tag">Optional</span></h3><p class="dim">Configure these now or skip this step and connect later.</p><div class="dialogfields">${createFields(f=>['gitName','gitEmail'].includes(f.key)||f.key.startsWith('connect:'))}</div>${!chains.includes('azure')?'<p class="dim">Azure and Azure DevOps require the azure toolchain in the chosen image.</p>':''}<div class="note"><strong class="amber">Connect workflow / to be defined</strong><p>Interactive sign-in requires SSH port forwarding and a new workflow. These options only simulate connection state. Git name and email must both be provided or both left blank.</p></div>`;
 }
 if(w.step===3)body=createReview();
 if(w.step===4)body=`<div class="create-success"><h3 class="green">✓ ${safe(w.result.name)} created</h3>${dl([['Instance',w.instanceName],['Status',w.result.status],['Image',image(w.result.image).name],['Agents',w.result.agents.join(', ')],['Workspace',w.result.workspace]])}<div class="note">The sandbox is ready in the selected instance. All changes remain in the fake host’s memory.</div></div>`;
 $('create-content').innerHTML=body;$('create-error').textContent=w.error??'';
 if(w.step===2){const chains=w.mode==='existing'?(createImage()?.toolchains??[]):createChains();for(const key of ['azure','azdo'])$('create-form').elements.namedItem('connect:'+key).disabled=!chains.includes('azure');}
 for(const element of $('create-form').querySelectorAll('input,select,button'))element.disabled=element.disabled||loading;
 $('create-back').hidden=w.step===0||w.step===4;$('create-back').disabled=loading;
 $('create-skip').hidden=w.step!==2;$('create-skip').disabled=loading;
 $('create-cancel').disabled=loading;$('create-cancel').textContent=w.step===4?'Close':'Cancel';
 $('create-next').disabled=loading;$('create-next').textContent=loading?'◌ Working …':w.step===0?'Next →':w.step===1?'Integrations →':w.step===2?'Review →':w.step===4?'Open sandbox':w.mode==='new'&&!createImage()?'Build image & create sandbox':'Create sandbox';
 restoreFocus(focus);
}
async function openCreateWizard(){
 if(loading||!state)return;loading=true;render();
 try{
  const form=await api('/api/form?action=create-sandbox');
  createWizard={step:0,form,values:Object.fromEntries(form.fields.map(f=>[f.key,f.value])),mode:images().length?'existing':'new',imageID:activeImage()?.id??'',instance:instanceID,instanceName:state.instance.name,group:state.instance.group,error:''};
  loading=false;renderCreateWizard();$('create-wizard').showModal();document.querySelector('#create-content button')?.focus();
 }catch(error){notify(error.message,true);}finally{loading=false;render();}
}
async function createNext(skip=false){
 if(!createWizard||loading)return;captureCreateValues();const w=createWizard;
 if(w.instance!==instanceID){$('create-error').textContent='Instance changed. Close the wizard and try again.';return;}
 w.error='';
 if(w.step===4){$('create-wizard').close();return;}
 if(w.step===0){
  if(w.mode==='existing'&&!createImage()){w.error='Select an image on this instance.';renderCreateWizard();return;}
  w.step=1;renderCreateWizard();document.querySelector('.create-body')?.scrollTo(0,0);$('create-form').elements.namedItem('name')?.focus();return;
 }
 if(!w.form.fields.some(f=>f.key.startsWith('agent:')&&w.values[f.key]==='true')){w.error='Select at least one agent.';if(w.step!==1)w.step=1;renderCreateWizard();return;}
 if(skip){for(const key of Object.keys(w.values))if(key.startsWith('connect:')||key==='gitName'||key==='gitEmail')w.values[key]=key.startsWith('connect:')?'false':'';}
 loading=true;renderCreateWizard();
 try{
  const payload=createPayload(w.step===1);
  if(w.step<3){state=await api('/api/review',payload,w.instance);w.step++;}
  else{
   state=await api('/api/command',payload,w.instance);w.result=state.sandboxes.find(b=>b.name===payload.values.name);w.step=4;target=w.result.name;section=0;page=0;imageID=w.result.image;statusFilter='all';$('search').value='';notify(state.events[0]?.text??'Sandbox created');
  }
 }catch(error){w.error=error.message;}
 finally{loading=false;render();renderCreateWizard();document.querySelector('.create-body')?.scrollTo(0,0);}
}
function createBack(){if(!createWizard||loading)return;captureCreateValues();createWizard.step=Math.max(0,createWizard.step-1);createWizard.error='';renderCreateWizard();document.querySelector('.create-body')?.scrollTo(0,0);}
$('create-form').onsubmit=event=>{event.preventDefault();call(createNext());};
$('create-next').onclick=()=>call(createNext());$('create-back').onclick=createBack;$('create-skip').onclick=()=>call(createNext(true));
$('create-cancel').onclick=()=>{if(!loading)$('create-wizard').close();};
$('create-wizard').addEventListener('cancel',event=>{if(loading)event.preventDefault();});
$('create-wizard').addEventListener('close',()=>{createWizard=null;});
$('create-content').addEventListener('click',event=>{
 const mode=event.target.closest('[data-create-mode]');if(mode&&!loading){captureCreateValues();createWizard.mode=mode.dataset.createMode;renderCreateWizard();return;}
 const img=event.target.closest('[data-create-image]');if(img&&!loading){captureCreateValues();createWizard.imageID=img.dataset.createImage;renderCreateWizard();}
});
$('create-form').addEventListener('change',()=>{captureCreateValues();if(createWizard?.step===0)renderCreateWizard();});
